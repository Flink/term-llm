package stream

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"

	"github.com/term-llm/term-llm/internal/llm/core"
)

// A cancelled stream context must not turn a failed run into a clean EOF. The
// consumer reports cancellation only while the run goroutine is still live, so a
// dropped error made a failed run look successful whenever the goroutine
// finished first.
func TestNewEventStreamKeepsRunErrorWhenStreamContextIsCancelled(t *testing.T) {
	wantErr := errors.New("provider failed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	st := New(ctx, func(ctx context.Context, send Sender) error {
		return wantErr
	})

	// Recv after the run goroutine has closed its channels, which is the ordering
	// that previously reported io.EOF.
	<-st.(*channelStream).done

	event, err := st.Recv()
	if err != nil {
		t.Fatalf("Recv() error = %v, want nil with a terminal error event", err)
	}
	if event.Type != core.EventError {
		t.Fatalf("Recv() event type = %v, want %v", event.Type, core.EventError)
	}
	if !errors.Is(event.Err, wantErr) {
		t.Fatalf("Recv() event error = %v, want %v", event.Err, wantErr)
	}
}

func TestStreamShutdownRacesWithRoutedTrySend(t *testing.T) {
	ready := make(chan Sender, 1)
	finish := make(chan struct{})
	st := New(t.Context(), func(_ context.Context, send Sender) error {
		ready <- send
		<-finish
		return nil
	})
	sender := <-ready
	routed := NewRoutedSender(func() Sender { return sender })
	sent := make(chan bool, 1)
	go func() {
		<-finish
		sent <- routed.TrySend(core.Event{Type: core.EventTextDelta, Text: "last"})
	}()
	close(finish)
	if <-sent {
		event, err := st.Recv()
		if err != nil || event.Text != "last" {
			t.Fatalf("accepted event was not delivered: (%+v, %v)", event, err)
		}
	}
	if _, err := st.Recv(); err != io.EOF {
		t.Fatalf("Recv() after shutdown = %v, want EOF", err)
	}
	if routed.TrySend(core.Event{}) {
		t.Fatal("retired sender accepted an event")
	}
}

func TestStreamShutdownUnblocksSendAndDrainsBuffer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, cancelStream := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			finish := make(chan struct{})
			ready := make(chan Sender, 1)
			wantErr := errors.New("provider failed with a full buffer")
			st := New(ctx, func(_ context.Context, send Sender) error {
				for i := 0; i < cap(send.Chan()); i++ {
					_ = send.Send(core.Event{Type: core.EventTextDelta, Text: "buffered"})
				}
				ready <- send
				<-finish
				return wantErr
			})
			send := <-ready
			result := make(chan error, 1)
			go func() { result <- send.Send(core.Event{}) }()
			synctest.Wait() // The send is parked on the full event buffer.
			wantSendErr := ErrClosed
			if cancelStream {
				cancel()
				wantSendErr = context.Canceled
			}
			close(finish)
			// Wait for producer shutdown without cancelling a normally closed stream.
			<-st.(*channelStream).done
			if err := <-result; !errors.Is(err, wantSendErr) {
				t.Errorf("Send() error = %v, want %v", err, wantSendErr)
			}
			for i := 0; i < cap(send.Chan()); i++ {
				if event, err := st.Recv(); err != nil || event.Text != "buffered" {
					t.Errorf("buffered event %d: (%+v, %v)", i, event, err)
				}
			}
			if event, err := st.Recv(); err != nil || event.Type != core.EventError || !errors.Is(event.Err, wantErr) {
				t.Errorf("terminal event: (%+v, %v), want %v", event, err, wantErr)
			}
			if _, err := st.Recv(); err != io.EOF {
				t.Errorf("final Recv() error = %v, want EOF", err)
			}
			cancel()
		}
	})
}
