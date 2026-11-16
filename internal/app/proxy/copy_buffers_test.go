package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func assertClearedCopyBuffer(t *testing.T, b []byte) {
	t.Helper()
	if len(b) != copyBufferSize || cap(b) != copyBufferSize {
		t.Fatalf("copy buffer shape = %d/%d, want %d/%d", len(b), cap(b), copyBufferSize, copyBufferSize)
	}
	for i, value := range b {
		if value != 0 {
			t.Fatalf("proxied data retained at buffer offset %d", i)
		}
	}
}

func TestCopyBufferPoolClearsEntireBufferAndReuses(t *testing.T) {
	pool := newCopyBufferPool(1)
	b := pool.Get()
	assertClearedCopyBuffer(t, b)
	for i := range b {
		b[i] = 0xff
	}
	pool.Put(b[:7])
	// A caller retaining its original slice must observe clearing before Put
	// returns, including bytes outside the shortened slice passed to Put.
	assertClearedCopyBuffer(t, b)
	reused := pool.Get()
	if &reused[0] != &b[0] {
		t.Fatal("returned buffer was not reused")
	}
	assertClearedCopyBuffer(t, reused)
}

func TestCopyBufferPoolBoundsRetentionAndNeverWaits(t *testing.T) {
	pool := newCopyBufferPool(2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 5 {
			b := bytes.Repeat([]byte{0xff}, copyBufferSize)
			pool.Put(b)
			for _, value := range b {
				if value != 0 {
					t.Error("full pool discarded uncleared data")
					return
				}
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("returning a buffer to a full pool blocked")
	}
	if len(pool.buffers) != 2 {
		t.Fatalf("retained %d buffers, want bounded capacity 2", len(pool.buffers))
	}
	// A third simultaneous checkout allocates rather than waiting for reuse.
	for range 3 {
		assertClearedCopyBuffer(t, pool.Get())
	}
}

func TestCopyBufferPoolRejectsForeignSizes(t *testing.T) {
	pool := newCopyBufferPool(2)
	for _, size := range []int{0, copyBufferSize / 2, copyBufferSize * 2} {
		b := bytes.Repeat([]byte{0xff}, size)
		pool.Put(b)
		if len(pool.buffers) != 0 {
			t.Fatalf("retained foreign buffer of size %d", size)
		}
		for _, value := range b {
			if value != 0 {
				t.Fatalf("discarded foreign buffer of size %d was not cleared", size)
			}
		}
	}
	pool.Put(nil)
}

func TestCopyBufferPoolsAreServiceLocalAndConcurrencyBounded(t *testing.T) {
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin")
	if err := os.WriteFile(admin, bytes.Repeat([]byte("a"), 64), 0600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, 2} {
		s, err := Open(Options{Auth: "token", StateDir: filepath.Join(t.TempDir(), "state"), AdminTokenFile: admin, MaxConcurrent: limit})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if cap(s.copyBuffers.buffers) != s.opts.MaxConcurrent || len(s.copyBuffers.buffers) != 0 {
			t.Fatal("service did not create a lazy, concurrency-bounded pool")
		}
		b := s.copyBuffers.Get()
		s.copyBuffers.Put(b)
		other := newCopyBufferPool(1)
		if fresh := other.Get(); &fresh[0] == &b[0] {
			t.Fatal("independent pools shared a response buffer")
		}
	}
}

func TestCopyBufferPoolConcurrentIsolation(t *testing.T) {
	pool := newCopyBufferPool(4)
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 16 {
				b := pool.Get()
				if len(b) != copyBufferSize || cap(b) != copyBufferSize || bytes.Count(b, []byte{0}) != copyBufferSize {
					t.Error("concurrent checkout received retained data or a nonstandard buffer")
					return
				}
				for i := range b {
					b[i] = 0xff
				}
				pool.Put(b)
			}
		})
	}
	workers.Wait()
	if len(pool.buffers) > 4 {
		t.Fatal("concurrent reuse exceeded the retention bound")
	}
}

func TestForwardReturnsClearedCopyBuffer(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "partial"}[partial], func(t *testing.T) {
			payload := bytes.Repeat([]byte("opaque private response"), 4000)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if partial {
					w.Header().Set("Content-Length", "100")
					w.Write([]byte("private partial"))
					return
				}
				w.Write(payload)
			}))
			defer up.Close()
			s := testService(t)
			configure(t, s, Config{Routes: []Route{route("r", "/r/", up.URL, "X-Local", "key")}}, map[string]material{"key": boundMaterial(t, up.URL, "fixture")})
			token := caller(t, s, "r")
			r := httptest.NewRequest("GET", "/r/opaque", nil).WithContext(context.WithValue(context.Background(), http.ServerContextKey, &http.Server{}))
			r.Header.Set("X-Local", "Local "+token)
			out := httptest.NewRecorder()
			func() {
				defer func() {
					p := recover()
					if (partial && p != http.ErrAbortHandler) || (!partial && p != nil) {
						t.Errorf("unexpected response-copy panic: %v", p)
					}
				}()
				s.ServeHTTP(out, r)
			}()
			if !partial && !bytes.Equal(out.Body.Bytes(), payload) {
				t.Fatal("buffer reuse altered opaque response bytes")
			}
			if len(s.copyBuffers.buffers) != 1 {
				t.Fatal("response copy did not return its buffer")
			}
			assertClearedCopyBuffer(t, s.copyBuffers.Get())
		})
	}
}
