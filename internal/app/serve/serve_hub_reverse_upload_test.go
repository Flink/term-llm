package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/term-llm/term-llm/internal/hub"
)

// Pace WebSocket writes below the old 20 MiB/10s threshold. Crucially, a giant
// Gorilla write would hold its control-frame mutex during this entire wait;
// bounded fragments instead yield that mutex between each paced network write.
type throttledUploadConn struct {
	net.Conn
	enabled         atomic.Bool
	maxWrite        atomic.Int64
	maxDeadlineWait atomic.Int64
	deadlineSets    atomic.Int64
	started         chan struct{}
	closed          chan struct{}
	startOnce       sync.Once
	closeOnce       sync.Once
}

func (c *throttledUploadConn) Write(p []byte) (int, error) {
	if c.enabled.Load() {
		for old := c.maxWrite.Load(); int64(len(p)) > old && !c.maxWrite.CompareAndSwap(old, int64(len(p))); old = c.maxWrite.Load() {
		}
		// The encoded upload takes about 36s of virtual time at 768 KiB/s.
		// Gorilla holds its write mutex during this delay, just as for slow I/O.
		timer := time.NewTimer(time.Duration(len(p)) * time.Second / (768 << 10))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	n, err := c.Conn.Write(p)
	if n > 0 && c.enabled.Load() {
		c.startOnce.Do(func() { close(c.started) })
	}
	return n, err
}

func (c *throttledUploadConn) SetWriteDeadline(d time.Time) error {
	if c.enabled.Load() && !d.IsZero() {
		wait := int64(time.Until(d))
		for old := c.maxDeadlineWait.Load(); wait > old && !c.maxDeadlineWait.CompareAndSwap(old, wait); old = c.maxDeadlineWait.Load() {
		}
		c.deadlineSets.Add(1)
	}
	return c.Conn.SetWriteDeadline(d)
}

func (c *throttledUploadConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// Wrap the transport at upgrade so the real HTTP server, Hub handler and
// Gorilla handshake all run over httptest's in-memory network.
type throttledUploadResponseWriter struct{ http.ResponseWriter }

func (w throttledUploadResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return &throttledUploadConn{Conn: conn, started: make(chan struct{}), closed: make(chan struct{})}, rw, nil
}

func TestHubReverseLargeCanceledUploadKeepsDefaultHeartbeatAndSibling(t *testing.T) {
	synctest.Test(t, testHubReverseLargeCanceledUpload)
}

func testHubReverseLargeCanceledUpload(t *testing.T) {
	const uploadSize = 20 << 20
	node := hub.Node{ID: "slow-upload", Connection: "reverse", BasePath: "/chat", Token: "token"}
	s := newHubServer(hub.NewRegistry(fakeHubResolver{nodes: []hub.Node{node}}), nil)
	node, _ = s.registry.Lookup(node.ID)
	handler := s.handler()
	ts := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(throttledUploadResponseWriter{w}, r)
	}))
	dialer := websocket.Dialer{
		NetDialContext:   ts.Client().Transport.(*http.Transport).DialContext,
		HandshakeTimeout: 10 * time.Second,
	}
	peer, _, err := dialer.Dial("ws://example.com/api/connect?node_id="+node.ID, http.Header{"Authorization": {"Bearer token"}, hubNodeIDHeader: {node.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	synctest.Wait() // Let registration and the heartbeat ticker start before the upload.
	c, err := s.reverse.connection(node)
	if err != nil {
		t.Fatal(err)
	}
	defer c.closeWithError(errors.New("throttled test complete"))
	socket := c.conn.UnderlyingConn().(*throttledUploadConn)
	beforePing := c.lastSeenValue()
	ping := make(chan struct{})
	var pingOnce sync.Once
	defaultPing := peer.PingHandler()
	peer.SetPingHandler(func(data string) error { err := defaultPing(data); pingOnce.Do(func() { close(ping) }); return err })
	type observedFrame struct {
		kind, id, path string
		size           int
	}
	frames := make(chan observedFrame, 8)
	peerFailure := make(chan error, 1)
	go func() {
		for {
			var frame hubReverseRequest
			// Read to the end of the WebSocket message, processing any
			// interleaved control frames, before accepting the JSON request.
			_, data, err := peer.ReadMessage()
			if err == nil {
				err = json.Unmarshal(data, &frame)
			}
			if err != nil {
				peerFailure <- err
				return
			}
			frames <- observedFrame{frame.Type, frame.ID, frame.Path, len(frame.Body)}
			if frame.Path == "/sibling" {
				if err := peer.WriteJSON(hubReverseResponse{ID: frame.ID, Status: 200, Body: []byte("ok")}); err != nil {
					peerFailure <- err
					return
				}
			}
		}
	}()
	socket.enabled.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := containmentDo(s, ctx, node, "/upload", bytes.Repeat([]byte("x"), uploadSize))
	select {
	case <-socket.started:
	case <-time.After(10 * time.Second):
		t.Fatal("upload never entered socket")
	}
	cancel()
	sibling := containmentDo(s, t.Context(), node, "/sibling", nil)
	// Caller cancellation must not truncate the JSON message. The unchanged
	// production heartbeat must cross that IN-PROGRESS message and receive pong.
	select {
	case <-ping:
	case err := <-peerFailure:
		t.Fatal(err)
	case <-time.After(hubReversePingInterval + 10*time.Second):
		t.Fatal("default ping starved behind large JSON writer")
	}
	synctest.Wait() // Process the pong without advancing virtual time to another fragment.
	select {
	case err := <-first:
		t.Fatalf("upload finished before heartbeat interleaved: %v", err)
	default:
	}
	if !c.lastSeenValue().After(beforePing) {
		t.Fatal("default pong did not refresh heartbeat")
	}
	if !s.reverse.isConnected(node.ID) {
		t.Fatal("canceled in-flight upload detached shared tunnel")
	}
	if err := containmentAwaitWithin(t, first, 70*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("upload error=%v", err)
	}
	if err := containmentAwaitWithin(t, sibling, 30*time.Second); err != nil {
		t.Fatalf("sibling failed after canceled upload: %v", err)
	}
	var uploadID string
	gotCancel, gotSibling := false, false
	watchdog := time.After(5 * time.Second)
	for !gotCancel || !gotSibling {
		select {
		case frame := <-frames:
			switch {
			case frame.path == "/upload":
				if frame.size != uploadSize {
					t.Fatalf("partial original reached remote: %d", frame.size)
				}
				uploadID = frame.id
			case frame.kind == hubReverseFrameCancel:
				if uploadID == "" || frame.id != uploadID {
					t.Fatalf("remote cancel before complete original: %#v upload=%s", frame, uploadID)
				}
				gotCancel = true
			case frame.path == "/sibling":
				gotSibling = true
			}
		case err := <-peerFailure:
			t.Fatal(err)
		case <-watchdog:
			t.Fatalf("remote cancellation/sibling missing: cancel=%v sibling=%v", gotCancel, gotSibling)
		}
	}
	if socket.maxDeadlineWait.Load() > int64(hubReverseWriteWait) || socket.deadlineSets.Load() < 10 {
		t.Fatalf("fragment deadlines not refreshed/bounded: max=%v updates=%d", time.Duration(socket.maxDeadlineWait.Load()), socket.deadlineSets.Load())
	}

	if socket.maxWrite.Load() > (32<<10)+4096 {
		t.Fatalf("unbounded Gorilla socket write=%d", socket.maxWrite.Load())
	}
	synctest.Wait() // The cancel writer may still be releasing its slot.
	if current, err := s.reverse.connection(node); err != nil || current != c {
		t.Fatalf("healthy sibling did not reuse the original tunnel: %v", err)
	}
	c.pendingMu.Lock()
	pending := len(c.pending)
	c.pendingMu.Unlock()
	if pending != 0 || len(c.active) != 0 {
		t.Fatalf("cancellation retained pending=%d active=%d", pending, len(c.active))
	}
}
