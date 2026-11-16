package proxy

const copyBufferSize = 32 << 10

// copyBufferPool bounds response-copy storage to one fixed-size buffer per
// admission slot. It never grows buffers to match a request or response body.
// Buffers contain proxied data, so Put clears them even when the pool is full.
// The pool is service-local; neither operation waits for a buffer to be returned.
type copyBufferPool struct {
	buffers chan []byte
}

func newCopyBufferPool(limit int) *copyBufferPool {
	return &copyBufferPool{buffers: make(chan []byte, limit)}
}

func (p *copyBufferPool) Get() []byte {
	select {
	case b := <-p.buffers:
		return b
	default:
		return make([]byte, copyBufferSize)
	}
}

func (p *copyBufferPool) Put(b []byte) {
	if cap(b) != copyBufferSize {
		clear(b)
		return
	}
	// Clear the whole buffer, not merely a possibly shortened visible slice.
	b = b[:copyBufferSize]
	clear(b)
	select {
	case p.buffers <- b:
	default:
	}
}
