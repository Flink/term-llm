package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPayloadDebugZeroOptionsUsesDefaultArchiveLimit(t *testing.T) {
	s := testService(t)
	defer s.Close()
	status, err := s.payloadDebugStatus()
	if err != nil || status.MaxTotalBytes != DefaultDebugPayloadMaxTotalBytes {
		t.Fatalf("zero option total default=%d, want %d (err=%v)", status.MaxTotalBytes, DefaultDebugPayloadMaxTotalBytes, err)
	}
}

func TestPayloadDebugTogglePreservesQuotaAndCLIOptions(t *testing.T) {
	s := testService(t)
	s.opts.DebugPayloadMaxBytes = 8
	s.opts.DebugPayloadMaxTotalBytes = debugMetadataLimit + 8
	if err := s.setPayloadDebugEnabled(true); err != nil {
		t.Fatal(err)
	}
	d := s.payloadDebug
	rec, req := debugRequest("px_first")
	c := s.beginPayloadCapture(rec, req)
	c.observe("request.body", []byte("12345678"), false, false, false)
	if err := s.setPayloadDebugEnabled(false); err != nil {
		t.Fatal(err)
	}
	m := debugManifest(t, c)
	if !m.Finished || m.Outcome != "debug_capture_disabled" {
		t.Fatal(m)
	}
	if s.beginPayloadCapture(rec, req) != nil {
		t.Fatal("disabled capture started")
	}
	if err := s.setPayloadDebugEnabled(true); err != nil {
		t.Fatal(err)
	}
	if s.payloadDebug != d {
		t.Fatal("toggle replaced the archive manager")
	}
	status, err := s.payloadDebugStatus()
	if err != nil || status.RecordCount != 1 || status.RemainingBytes != 0 {
		t.Fatal(status, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(s.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	status, err = restarted.payloadDebugStatus()
	if err != nil || status.Enabled || status.RecordCount != 1 || restarted.opts.DebugPayloadDir != "" {
		t.Fatal("runtime enable persisted", status, err)
	}
}

func TestPayloadDebugCustomDirectoryAndOffLimits(t *testing.T) {
	s := testService(t)
	opts := s.opts
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []Options{
		{DebugPayloadMaxBytes: -1}, {DebugPayloadMaxBytes: 64<<20 + 1},
		{DebugPayloadMaxTotalBytes: -1}, {DebugPayloadMaxTotalBytes: 1}, {DebugPayloadMaxTotalBytes: 4<<30 + 1},
	} {
		invalid := opts
		invalid.DebugPayloadMaxBytes = tt.DebugPayloadMaxBytes
		invalid.DebugPayloadMaxTotalBytes = tt.DebugPayloadMaxTotalBytes
		if opened, err := Open(invalid); err == nil {
			opened.Close()
			t.Fatal("invalid off-mode limits accepted", tt)
		}
	}
	opts.DebugPayloadDir = filepath.Join(t.TempDir(), "custom")
	custom, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer custom.Close()
	status, err := custom.payloadDebugStatus()
	if err != nil || !status.Enabled || status.Directory != opts.DebugPayloadDir {
		t.Fatal(status, err)
	}
	if err := custom.setPayloadDebugEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := custom.setPayloadDebugEnabled(true); err != nil {
		t.Fatal(err)
	}
	status, err = custom.payloadDebugStatus()
	if err != nil || status.Directory != opts.DebugPayloadDir {
		t.Fatal(status, err)
	}
}

func TestPayloadDebugDisableDoesNotInterruptSSE(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: before\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: after\n\n")
	}))
	defer up.Close()
	defer unblock()
	s := testService(t)
	configure(t, s, Config{Routes: []Route{route("r", "/r/", up.URL, "X-Local", "key")}}, map[string]material{"key": boundMaterial(t, up.URL, "private")})
	token := caller(t, s, "r")
	if err := s.setPayloadDebugEnabled(true); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeHTTP(w, r); close(done) }))
	defer func() { unblock(); srv.Close() }()
	req, _ := http.NewRequest("GET", srv.URL+"/r/stream", nil)
	req.Header.Set("X-Local", "Local "+token)
	client := srv.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	prefix := make([]byte, len("data: before\n\n"))
	if _, err := io.ReadFull(response.Body, prefix); err != nil {
		t.Fatal(err)
	}
	if err := s.setPayloadDebugEnabled(false); err != nil {
		t.Fatal(err)
	}
	id := response.Header.Get("X-Term-LLM-Proxy-Request-ID")
	path := filepath.Join(s.payloadDebug.root, id, "response.body")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	after, err := io.ReadAll(response.Body)
	if err != nil || string(after) != "data: after\n\n" {
		t.Fatal("stream interrupted", string(after), err)
	}
	<-done
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, before) {
		t.Fatal("capture continued after disable", string(stored), err)
	}
	data, err := os.ReadFile(filepath.Join(s.payloadDebug.root, id, "manifest.json"))
	if err != nil || !bytes.Contains(data, []byte(`"outcome": "debug_capture_disabled"`)) {
		t.Fatal(string(data), err)
	}
}

func TestPayloadDebugDisableDoesNotInterruptWebSocket(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw)
	}))
	defer up.Close()
	s := testService(t)
	route := route("ws", "/ws/", up.URL, "X-Local", "key")
	route.WebSocket = true
	configure(t, s, Config{Routes: []Route{route}}, map[string]material{"key": boundMaterial(t, up.URL, "private")})
	token := caller(t, s, "ws")
	if err := s.setPayloadDebugEnabled(true); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeHTTP(w, r); close(done) }))
	defer srv.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /ws/responses HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nX-Local: Local %s\r\n\r\n", token)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatal(resp, err)
	}
	wire := []byte{0x82, 0x82, 1, 2, 3, 4, 5, 6}
	echo := func() {
		t.Helper()
		if _, err := conn.Write(wire); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(wire))
		if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, wire) {
			t.Fatal("changed frames", got, err)
		}
	}
	echo()
	if err := s.setPayloadDebugEnabled(false); err != nil {
		t.Fatal(err)
	}
	id := resp.Header.Get("X-Term-LLM-Proxy-Request-ID")
	dir := filepath.Join(s.payloadDebug.root, id)
	for _, name := range []string{"websocket-client.frames", "websocket-server.frames"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(data, wire) {
			t.Fatal(name, data, err)
		}
	}
	echo()
	if err := s.setPayloadDebugEnabled(true); err != nil {
		t.Fatal(err)
	}
	echo() // Re-enabling must not resume capture of this already-finalized socket.
	conn.Close()
	<-done
	for _, name := range []string{"websocket-client.frames", "websocket-server.frames"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(data, wire) {
			t.Fatal("capture resumed", name, data, err)
		}
	}
}

func TestPayloadDebugConcurrentToggleCaptureAndClose(t *testing.T) {
	s := testService(t)
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				switch worker % 3 {
				case 0:
					_ = s.setPayloadDebugEnabled(i%2 == 0)
				case 1:
					rec, req := debugRequest(fmt.Sprintf("px_%d_%d", worker, i))
					c := s.beginPayloadCapture(rec, req)
					c.observe("request.body", []byte("body"), false, false, false)
					c.finish(rec)
				case 2:
					_, _ = s.payloadDebugStatus()
				}
			}
		}(worker)
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Close() }()
	wg.Wait()
	if err := s.setPayloadDebugEnabled(true); err == nil {
		t.Fatal("enabled after close")
	}
	if err := s.Close(); err != nil {
		t.Fatal("repeated close", err)
	}
}

func TestPayloadDebugConcurrentLazyInitialization(t *testing.T) {
	s := testService(t)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 9; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			for i := 0; i < 20; i++ {
				switch worker % 3 {
				case 0:
					if err := s.setPayloadDebugEnabled(i%2 == 0); err != nil {
						t.Error(err)
					}
				case 1:
					rec, req := debugRequest(fmt.Sprintf("px_lazy_%d_%d", worker, i))
					c := s.beginPayloadCapture(rec, req)
					c.observe("request.body", []byte("prefix"), false, false, false)
					c.finish(rec)
				case 2:
					if _, err := s.payloadDebugStatus(); err != nil {
						t.Error(err)
					}
				}
			}
		}(worker)
	}
	close(start)
	workers.Wait()
	if s.payloadDebug == nil {
		t.Fatal("manager was never initialized")
	}
}

func TestPayloadDebugRejectsArchiveReplacementAcrossToggles(t *testing.T) {
	s := testService(t)
	if err := s.setPayloadDebugEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := s.setPayloadDebugEnabled(false); err != nil {
		t.Fatal(err)
	}
	root := s.payloadDebug.root
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.setPayloadDebugEnabled(true); err == nil {
		t.Fatal("archive replacement silently accepted")
	}
}
