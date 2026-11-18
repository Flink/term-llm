package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

func debugForTest(t *testing.T, perStream, total int64) *payloadDebugger {
	t.Helper()
	d, err := openPayloadDebugger(Options{DebugPayloadDir: filepath.Join(t.TempDir(), "payloads"), DebugPayloadMaxBytes: perStream, DebugPayloadMaxTotalBytes: total})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}
func debugManifest(t *testing.T, c *payloadCapture) payloadManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m payloadManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest: %v (%s)", err, data)
	}
	return m
}
func debugRequest(id string) (Record, *http.Request) {
	return Record{ID: id, Route: "fixture", Method: "POST", Started: 1, LocalPrefix: "/fixture/", Path: "/v1/messages"}, httptest.NewRequest("POST", "/fixture/v1/messages?secret-query=DO-NOT-CAPTURE", nil)
}
func TestPayloadDebugDisabledAndPrivateDirectory(t *testing.T) {
	d, err := openPayloadDebugger(Options{})
	if err != nil || d != nil {
		t.Fatal(d, err)
	}
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err == nil {
		if _, err := openPayloadDebugger(Options{DebugPayloadDir: link}); err == nil {
			t.Fatal("accepted symlink")
		}
	}
	for _, opts := range []Options{
		{DebugPayloadDir: root, DebugPayloadMaxBytes: -1},
		{DebugPayloadDir: root, DebugPayloadMaxTotalBytes: 1},
	} {
		if _, err := openPayloadDebugger(opts); err == nil {
			t.Fatal("invalid limits accepted")
		}
	}
	d = debugForTest(t, 32, 1<<20)
	if _, err := openPayloadDebugger(Options{DebugPayloadDir: d.root}); err == nil {
		t.Fatal("concurrent debug owner accepted")
	}
	if err := filepath.WalkDir(d.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return fileprivacy.CheckDir(st)
		}
		return fileprivacy.CheckFile(st)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPayloadDebugStreamsRemainByteExactAndBounded(t *testing.T) {
	d := debugForTest(t, 8, 1<<20)
	rec, req := debugRequest("px_fixture")
	req.Header.Set("Authorization", "DO-NOT-CAPTURE-AUTH")
	req.Header.Set("Cookie", "DO-NOT-CAPTURE-COOKIE")
	c := d.begin(rec, req)
	requestBody := []byte("request binary\x00bytes")
	reader := &payloadReadCloser{ReadCloser: io.NopCloser(bytes.NewReader(requestBody)), capture: c, stream: "request.body"}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, requestBody) {
		t.Fatal("request body changed", err)
	}
	_ = reader.Close()
	responseBody := []byte("data: response\x00bytes\n\n")
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Set-Cookie": {"DO-NOT-CAPTURE-RESPONSE-COOKIE"}}, Body: io.NopCloser(bytes.NewReader(responseBody))}
	c.wrapResponse(resp)
	got, err = io.ReadAll(resp.Body)
	if err != nil || !bytes.Equal(got, responseBody) {
		t.Fatal("response body changed", err)
	}
	_ = resp.Body.Close()
	rec.Ended, rec.Status, rec.Outcome = 2, 200, "complete"
	c.finish(rec)
	m := debugManifest(t, c)
	if !m.Finished || m.Status != 200 || m.Outcome != "complete" {
		t.Fatal(m)
	}
	for name, want := range map[string][]byte{"request.body": requestBody, "response.body": responseBody} {
		b, err := os.ReadFile(filepath.Join(c.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, want[:8]) {
			t.Fatalf("capture prefix changed: %q", b)
		}
		s := m.Streams[name]
		if s.BytesSeen != int64(len(want)) || s.BytesStored != 8 || !s.Truncated || !s.EOF {
			t.Fatal(s)
		}
	}
	_ = filepath.WalkDir(c.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(b, []byte("DO-NOT-CAPTURE")) {
			t.Fatal("captured a query/header")
		}
		return nil
	})
}

func TestPayloadDebugAggregateQuotaProtectsActiveCapture(t *testing.T) {
	d := debugForTest(t, 100, debugMetadataLimit+12)
	rec, req := debugRequest("px_quota")
	c := d.begin(rec, req)
	c.observe("request.body", []byte("abcdefghij"), true, false, false)
	c.observe("response.body", []byte("klmnopqrst"), true, false, false)
	next, _ := debugRequest("px_second")
	if d.begin(next, req) != nil {
		t.Fatal("archive admitted a new capture with no inactive capture to evict")
	}
	c.finish(rec)
	m := debugManifest(t, c)
	if m.Streams["request.body"].BytesStored != 10 || m.Streams["response.body"].BytesStored != 2 || !m.Streams["response.body"].Truncated {
		t.Fatal(m.Streams)
	}
}

func TestPayloadDebugReadAndCaptureErrorsDoNotChangeTransport(t *testing.T) {
	d := debugForTest(t, 32, 1<<20)
	rec, req := debugRequest("px_error")
	c := d.begin(rec, req)
	expected := errors.New("transport read failed")
	body := &payloadReadCloser{ReadCloser: &usageTestBody{Reader: &usageErrorReader{data: "partial", err: expected}}, capture: c, stream: "response.body"}
	_, err := io.ReadAll(body)
	if !errors.Is(err, expected) {
		t.Fatalf("read error changed: %v", err)
	}
	c.observe("request.body", []byte("first"), false, false, false)
	if err := c.manifest.Streams["request.body"].file.Close(); err != nil {
		t.Fatal(err)
	}
	c.observe("request.body", []byte("next"), false, false, false)
	c.finish(rec)
	m := debugManifest(t, c)
	if !m.Streams["response.body"].ReadError || m.Streams["response.body"].EOF || !m.Streams["request.body"].CaptureError {
		t.Fatal(m.Streams)
	}
}

type debugDuplexFixture struct {
	io.Reader
	bytes.Buffer
}

func (b *debugDuplexFixture) Read(p []byte) (int, error) { return b.Reader.Read(p) }
func (b *debugDuplexFixture) Close() error               { return nil }
func TestPayloadDebugWebsocketAndConcurrentShutdown(t *testing.T) {
	d := debugForTest(t, 4096, 1<<20)
	rec, req := debugRequest("px_socket")
	c := d.begin(rec, req)
	serverFrames := []byte{0x81, 2, 'o', 'k', 0x88, 0}
	raw := &debugDuplexFixture{Reader: bytes.NewReader(serverFrames)}
	resp := &http.Response{StatusCode: 101, Header: http.Header{}, Body: raw}
	c.wrapResponse(resp)
	duplex, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatal("lost duplex")
	}
	got, err := io.ReadAll(duplex)
	if err != nil || !bytes.Equal(got, serverFrames) {
		t.Fatal("frames changed", err)
	}
	clientFrames := []byte{0x81, 0x82, 1, 2, 3, 4, 5, 6}
	if _, err := duplex.Write(clientFrames); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw.Buffer.Bytes(), clientFrames) {
		t.Fatal("client frames changed")
	}
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); c.observe("websocket-client.frames", []byte{1}, false, false, false) }()
	}
	workers.Add(1)
	go func() { defer workers.Done(); _ = d.Close() }()
	workers.Wait()
	c.finish(rec) // idempotent after shutdown has finalized and closed all writers
	m := debugManifest(t, c)
	if !m.WebSocket || m.Outcome != "debug_capture_shutdown" || !m.Finished {
		t.Fatal(m)
	}
	b, err := os.ReadFile(filepath.Join(c.dir, "websocket-server.frames"))
	if err != nil || !bytes.Equal(b, serverFrames) {
		t.Fatal("server capture", err)
	}
}

func TestPayloadDebugServiceCapturesJSONAndReportedModel(t *testing.T) {
	body := `{"model":"requested-model","messages":[{"role":"user","content":"hello"}]}`
	reply := `{"type":"message","model":"reported-model","usage":{"input_tokens":11,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != body {
			t.Error("request changed")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}))
	defer up.Close()
	s := testService(t)
	s.payloadDebug = debugForTest(t, 4096, 1<<20)
	r := route("r", "/r/", up.URL, "X-Local", "key")
	r.Usage = "auto"
	configure(t, s, Config{Routes: []Route{r}}, map[string]material{"key": boundMaterial(t, up.URL, "private-upstream-key")})
	token := caller(t, s, "r")
	out := call(s, "POST", "/r/v1/messages?key=DO-NOT-CAPTURE", "X-Local", token, strings.NewReader(body))
	if out.Code != 200 || out.Body.String() != reply {
		t.Fatal(out.Code, out.Body.String())
	}
	id := out.Header().Get("X-Term-LLM-Proxy-Request-ID")
	data, err := os.ReadFile(filepath.Join(s.payloadDebug.root, id, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m payloadManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Model != "reported-model" || m.Usage == nil || m.Usage.Usage.Input != 11 || m.Usage.Usage.CacheWrite != 2 {
		t.Fatalf("missing model/usage: %s", data)
	}
	if bytes.Contains(data, []byte("DO-NOT-CAPTURE")) || bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte("private-upstream-key")) {
		t.Fatal("credential/query metadata leaked")
	}
}

func TestPayloadDebugDoesNotBufferSSEUntilCompletion(t *testing.T) {
	first := "data: first\n\n"
	release := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, first)
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: second\n\n")
	}))
	defer up.Close()
	s := testService(t)
	s.payloadDebug = debugForTest(t, 4096, 1<<20)
	configure(t, s, Config{Routes: []Route{route("r", "/r/", up.URL, "X-Local", "key")}}, map[string]material{"key": boundMaterial(t, up.URL, "fixture")})
	token := caller(t, s, "r")
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeHTTP(w, r); close(done) }))
	defer func() { unblock.Do(func() { close(release) }); srv.Close() }()
	client := srv.Client()
	client.Timeout = 5 * time.Second
	req, _ := http.NewRequest("GET", srv.URL+"/r/stream", nil)
	req.Header.Set("X-Local", "Local "+token)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	chunk := make([]byte, len(first))
	if _, err := io.ReadFull(response.Body, chunk); err != nil || string(chunk) != first {
		t.Fatal("first stream chunk was not forwarded", err)
	}
	id := response.Header.Get("X-Term-LLM-Proxy-Request-ID")
	capture, err := os.ReadFile(filepath.Join(s.payloadDebug.root, id, "response.body"))
	if err != nil || string(capture) != first {
		t.Fatal("first chunk not captured while stream is open", err)
	}
	unblock.Do(func() { close(release) })
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestPayloadDebugOpenFailureReleasesArchiveAndStore(t *testing.T) {
	root := t.TempDir()
	admin := filepath.Join(root, "admin")
	if err := os.WriteFile(admin, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Auth: "token", StateDir: filepath.Join(root, "state"), AdminTokenFile: admin, DebugPayloadDir: filepath.Join(root, "captures"), ConfigFile: filepath.Join(root, "missing.yaml")}
	if s, err := Open(opts); err == nil {
		s.Close()
		t.Fatal("missing configuration accepted")
	}
	opts.ConfigFile = ""
	s, err := Open(opts)
	if err != nil {
		t.Fatalf("failed startup retained resources: %v", err)
	}
	if s.payloadDebug == nil {
		t.Fatal("capture not enabled through options")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := openPayloadDebugger(opts)
	if err != nil {
		t.Fatalf("service shutdown retained archive lock: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPayloadDebugRejectsPublicArchiveWithoutChangingPermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if fileprivacy.CheckDir(info) == nil {
		t.Skip("platform does not use Unix directory permission bits")
	}
	if _, err := openPayloadDebugger(Options{DebugPayloadDir: root}); err == nil {
		t.Fatal("accepted public archive")
	}
	after, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != info.Mode().Perm() {
		t.Fatal("silently changed existing directory permissions")
	}
}
