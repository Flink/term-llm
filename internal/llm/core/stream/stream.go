// Package stream provides import-cycle-free event stream, SSE decoding, and
// typed stream error helpers shared by provider implementations.
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/term-llm/term-llm/internal/llm/core"
	"github.com/term-llm/term-llm/internal/restart"
)

// ErrClosed is returned by Sender.Send when the event stream has closed.
var ErrClosed = errors.New("stream closed")

// Sender provides safe event sending for stream producers. Copies of a
// stream-owned Sender share its shutdown state. Zero value is a no-op sender.
type Sender struct {
	ctx   context.Context
	ch    chan<- core.Event
	route func() Sender
	life  *sendLifetime
}

type sendLifetime struct {
	active  sync.RWMutex
	stopped chan struct{}
}

// stop wakes blocked sends and waits for them to leave the channel before it
// can be closed. Later sends see stopped while holding a read lock and do not
// touch the event channel. This signal is separate from consumer cancellation.
func (l *sendLifetime) stop() {
	close(l.stopped)
	l.active.Lock()
	l.active.Unlock()
}

// NewSender returns a Sender that delivers events to ch until ctx is done.
// A nil ch yields a no-op sender. The caller owns ch and must coordinate
// closure with senders; recovering a closed-channel panic is not synchronization.
func NewSender(ctx context.Context, ch chan<- core.Event) Sender {
	return Sender{ctx: ctx, ch: ch}
}

// NewRoutedSender resolves the destination for each event. The resolver must
// return a plain Sender and synchronize access to its destination. No relay
// goroutine is kept alive when an asynchronous producer changes turns.
func NewRoutedSender(resolve func() Sender) Sender { return Sender{route: resolve} }

func (s Sender) destination() Sender {
	if s.route != nil {
		return s.route()
	}
	return s
}

// Context returns the context bounding sends.
func (s Sender) Context() context.Context { return s.destination().ctx }

// Chan returns the underlying event channel (nil for a no-op sender). Sending
// on it directly, including via NewSender, bypasses stream shutdown protection.
// Such callers must finish all sends before the channel owner can close it.
func (s Sender) Chan() chan<- core.Event { return s.destination().ch }

// Send blocks until delivered, the context is cancelled, or the stream closes.
func (s Sender) Send(event core.Event) error {
	s = s.destination()
	if s.ch == nil || s.send(event, true) {
		return nil
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return ErrClosed
}

// TrySend attempts a non-blocking send and reports whether it was delivered.
func (s Sender) TrySend(event core.Event) bool {
	s = s.destination()
	return s.ch != nil && s.send(event, false)
}

func (s Sender) send(event core.Event, block bool) (sent bool) {
	var stopped <-chan struct{}
	if s.life != nil {
		if block {
			s.life.active.RLock()
		} else if !s.life.active.TryRLock() {
			return false
		}
		defer s.life.active.RUnlock()
		stopped = s.life.stopped
		select {
		case <-stopped:
			return false
		default:
		}
	} else {
		// Preserve the fallback for externally owned channels.
		defer func() {
			if recover() != nil {
				sent = false
			}
		}()
	}
	if block {
		select {
		case s.ch <- event:
			return true
		case <-s.ctx.Done():
			return false
		case <-stopped:
			return false
		}
	}
	select {
	case s.ch <- event:
		return true
	case <-s.ctx.Done():
		return false
	default:
		return false
	}
}

// SafeSend attempts to send an event to a caller-owned channel, returning
// false on closure or cancellation. The caller must still synchronize sends
// with channel closure; panic recovery alone does not prevent a data race.
func SafeSend(ctx context.Context, ch chan<- core.Event, event core.Event) bool {
	return NewSender(ctx, ch).send(event, true)
}

type channelStream struct {
	ctx         context.Context
	cancel      context.CancelFunc
	cancelHook  func()
	closeOnce   sync.Once
	events      <-chan core.Event
	terminalErr <-chan error
	done        <-chan struct{}
}

// New starts run in a goroutine and returns a core.Stream that yields the
// events run sends. A non-nil error from run is delivered as a terminal
// core.EventError.
func New(ctx context.Context, run func(context.Context, Sender) error) core.Stream {
	return NewWithCancelHook(ctx, nil, run)
}

// NewWithCancelHook is like New, with an extra hook
// invoked by Close after canceling the stream context. Providers that create
// blocking resources before the stream goroutine (for example, an HTTP response
// body whose request used the parent context) can use the hook to unblock reads.
func NewWithCancelHook(ctx context.Context, cancelHook func(), run func(context.Context, Sender) error) core.Stream {
	if cancelHook != nil {
		cancelHook = sync.OnceFunc(cancelHook)
	}
	ctx, release, ownershipErr := restart.Child(ctx)
	streamCtx, cancel := context.WithCancel(ctx)
	ch := make(chan core.Event, 16)
	terminalErr := make(chan error, 1)
	done := make(chan struct{})
	sender := Sender{ctx: streamCtx, ch: ch, life: &sendLifetime{stopped: make(chan struct{})}}
	go func() {
		if release != nil {
			defer release()
		}
		defer close(done)
		var runErr error
		defer func() {
			if r := recover(); r != nil && streamCtx.Err() == nil {
				runErr = fmt.Errorf("stream panic: %v", r)
			}
			sender.life.stop()
			if runErr != nil {
				// Keep the terminal event after accepted sends. Preserve it even
				// if the buffer is full or the consumer has cancelled.
				select {
				case ch <- core.Event{Type: core.EventError, Err: runErr}:
				default:
					terminalErr <- runErr
				}
			}
			close(terminalErr)
			close(ch)
		}()
		if ownershipErr != nil {
			if cancelHook != nil {
				cancelHook()
			}
			terminalErr <- ownershipErr
			return
		}
		runErr = run(streamCtx, sender)
	}()
	return &channelStream{ctx: streamCtx, cancel: cancel, cancelHook: cancelHook, events: ch, terminalErr: terminalErr, done: done}
}

func (s *channelStream) Recv() (core.Event, error) {
	// Non-blocking drain: consume any buffered event before checking ctx.Done().
	// This prevents dropping EventUsage/EventDone when ctx and events are both ready.
	select {
	case event, ok := <-s.events:
		if !ok {
			if err, ok := <-s.terminalErr; ok {
				return core.Event{Type: core.EventError, Err: err}, nil
			}
			return core.Event{}, io.EOF
		}
		return event, nil
	default:
	}

	select {
	case <-s.ctx.Done():
		return core.Event{}, s.ctx.Err()
	case event, ok := <-s.events:
		if !ok {
			if err, ok := <-s.terminalErr; ok {
				return core.Event{Type: core.EventError, Err: err}, nil
			}
			return core.Event{}, io.EOF
		}
		return event, nil
	}
}

func (s *channelStream) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		if s.cancelHook != nil {
			s.cancelHook()
		}
	})
	<-s.done
	return nil
}
