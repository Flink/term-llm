package proxy

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const DefaultDebugPayloadMaxTotalBytes int64 = 2560 << 20

const debugPreviewLimit = 64 << 10

func debugPayloadLimits(opts Options) (perStream, total int64, err error) {
	perStream, total = opts.DebugPayloadMaxBytes, opts.DebugPayloadMaxTotalBytes
	if perStream == 0 {
		perStream = 4 << 20
	}
	if total == 0 {
		total = DefaultDebugPayloadMaxTotalBytes
	}
	if perStream < 1 || perStream > 64<<20 || total < debugMetadataLimit || total > 4<<30 {
		err = fmt.Errorf("debug payload limits require 1 byte–64 MiB per direction and 32 KiB–4 GiB total")
	}
	return
}

func (s *Service) payloadDebugDirectory() string {
	if s.opts.DebugPayloadDir != "" {
		return s.opts.DebugPayloadDir
	}
	return filepath.Join(s.opts.StateDir, "debug-payloads")
}

// debugMu owns the pointer, but the stable debugger owns its enabled flag and
// lifetime quota. Never replace it when disabling/re-enabling capture.
func (s *Service) beginPayloadCapture(rec Record, req *http.Request) *payloadCapture {
	s.debugMu.Lock()
	defer s.debugMu.Unlock()
	if s.debugClosing {
		return nil
	}
	return s.payloadDebug.begin(rec, req)
}

// Caller holds debugMu. Inspection may open an existing archive, but must not
// create a directory or turn on recording as a side effect of a GET.
func (s *Service) initPayloadDebug(create bool) error {
	if s.payloadDebug != nil {
		return nil
	}
	if !create {
		if _, err := os.Lstat(s.payloadDebugDirectory()); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return fmt.Errorf("inspect debug payload directory: %w", err)
		}
	}
	opts := s.opts
	opts.DebugPayloadDir = s.payloadDebugDirectory()
	d, err := openPayloadDebugger(opts)
	if err != nil {
		return err
	}
	d.enabled = false
	s.payloadDebug = d
	return nil
}

func (s *Service) setPayloadDebugEnabled(enabled bool) error {
	s.debugMu.Lock()
	defer s.debugMu.Unlock()
	if s.debugClosing {
		return fmt.Errorf("proxy is shutting down")
	}
	if enabled {
		if err := s.initPayloadDebug(true); err != nil {
			return err
		}
		root, err := s.payloadDebug.openRoot()
		if err != nil {
			return err
		}
		root.Close()
	}
	if s.payloadDebug != nil {
		s.payloadDebug.setEnabled(enabled)
	}
	return nil
}

func (d *payloadDebugger) setEnabled(enabled bool) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.enabled = enabled
	var active []*payloadCapture
	if !enabled {
		for _, c := range d.active {
			active = append(active, c)
		}
	}
	d.mu.Unlock()
	// Never hold d.mu while taking c.mu: observers take these in reverse order.
	for _, c := range active {
		c.finish(Record{Ended: time.Now().UnixMilli(), Outcome: "debug_capture_disabled"})
	}
}

type payloadDebugStatus struct {
	Enabled           bool   `json:"enabled"`
	Directory         string `json:"directory"`
	MaxBytesPerStream int64  `json:"max_bytes_per_stream"`
	MaxTotalBytes     int64  `json:"max_total_bytes"`
	RemainingBytes    int64  `json:"remaining_bytes"`
	RecordCount       int    `json:"record_count"`
	RecordLimit       int    `json:"record_limit"`
	PreviewLimitBytes int    `json:"preview_limit_bytes"`
}

func (s *Service) payloadDebugStatus() (payloadDebugStatus, error) {
	s.debugMu.Lock()
	defer s.debugMu.Unlock()
	perStream, total, err := debugPayloadLimits(s.opts)
	status := payloadDebugStatus{Directory: s.payloadDebugDirectory(), MaxBytesPerStream: perStream, MaxTotalBytes: total, RecordLimit: debugRecordLimit, PreviewLimitBytes: debugPreviewLimit}
	if err != nil {
		return status, err
	}
	status.RemainingBytes = status.MaxTotalBytes
	if s.debugClosing {
		return status, fmt.Errorf("proxy is shutting down")
	}
	if err := s.initPayloadDebug(false); err != nil {
		return status, err
	}
	if d := s.payloadDebug; d != nil {
		d.mu.Lock()
		defer d.mu.Unlock()
		status.Enabled = d.enabled && !d.closed
		status.Directory, status.MaxBytesPerStream, status.MaxTotalBytes = d.root, d.perStream, d.total
		status.RemainingBytes, status.RecordCount = max(0, d.remaining), d.records
	}
	return status, nil
}

// Pin the archive identity across runtime toggles. Replacing a directory must
// never redirect capture/inspection or silently transfer its lifetime quota.
func (d *payloadDebugger) openRoot() (*os.Root, error) {
	root, err := openPrivateDebugRoot(d.root)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil || !os.SameFile(d.rootInfo, info) {
		root.Close()
		return nil, errUnsafeDebugFile
	}
	return root, nil
}
