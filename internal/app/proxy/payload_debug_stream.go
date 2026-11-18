package proxy

import (
	"io"
	"net/http"
)

// These wrappers never pre-read, replay, decompress, or change transport errors.
// Files contain only bytes consumed on the forwarding path, not HTTP headers.
type payloadReadCloser struct {
	io.ReadCloser
	capture *payloadCapture
	stream  string
}

func (b *payloadReadCloser) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.capture.observe(b.stream, p[:n], err == io.EOF, err != nil && err != io.EOF, false)
	return n, err
}

type payloadDuplex struct {
	io.ReadWriteCloser
	capture *payloadCapture
}

func (b *payloadDuplex) Read(p []byte) (int, error) {
	n, err := b.ReadWriteCloser.Read(p)
	b.capture.observe("websocket-server.frames", p[:n], err == io.EOF, err != nil && err != io.EOF, false)
	return n, err
}
func (b *payloadDuplex) Write(p []byte) (int, error) {
	n, err := b.ReadWriteCloser.Write(p)
	b.capture.observe("websocket-client.frames", p[:n], false, false, err != nil)
	return n, err
}

func (c *payloadCapture) wrapResponse(resp *http.Response) {
	if c == nil || resp.Body == nil {
		return
	}
	c.response(resp)
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if duplex, ok := resp.Body.(io.ReadWriteCloser); ok {
			resp.Body = &payloadDuplex{ReadWriteCloser: duplex, capture: c}
		}
	} else {
		resp.Body = &payloadReadCloser{ReadCloser: resp.Body, capture: c, stream: "response.body"}
	}
}
