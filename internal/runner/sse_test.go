package runner

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── Subscribe / Unsubscribe / Broadcast ────────────────────────────────────

func TestSSEBroadcaster_Broadcast_ReachesEverySubscriber(t *testing.T) {
	var b SSEBroadcaster // zero value must be usable

	first := b.Subscribe()
	second := b.Subscribe()
	defer b.Unsubscribe(first)
	defer b.Unsubscribe(second)

	b.Broadcast()

	for name, ch := range map[string]chan struct{}{"first": first, "second": second} {
		select {
		case <-ch:
		default:
			t.Errorf("subscriber %s did not receive the broadcast", name)
		}
	}
}

func TestSSEBroadcaster_Broadcast_NoSubscribersIsNoOp(t *testing.T) {
	var b SSEBroadcaster
	b.Broadcast() // must not panic on the nil subscriber map
}

func TestSSEBroadcaster_Broadcast_DropsEventWhenSubscriberBufferIsFull(t *testing.T) {
	var b SSEBroadcaster
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Broadcast()
	b.Broadcast() // must not block: the size-1 buffer is already full

	if len(ch) != 1 {
		t.Errorf("buffered events = %d, want 1 (second broadcast should be dropped)", len(ch))
	}
}

func TestSSEBroadcaster_Unsubscribe_StopsFurtherDelivery(t *testing.T) {
	var b SSEBroadcaster
	ch := b.Subscribe()
	b.Unsubscribe(ch)

	b.Broadcast()

	select {
	case <-ch:
		t.Error("unsubscribed channel still received a broadcast")
	default:
	}
}

func TestSSEBroadcaster_Unsubscribe_UnknownChannelIsNoOp(t *testing.T) {
	var b SSEBroadcaster
	stranger := make(chan struct{}, 1)

	b.Unsubscribe(stranger) // never subscribed — must not panic

	ch := b.Subscribe()
	defer b.Unsubscribe(ch)
	b.Unsubscribe(stranger) // subscriber map is non-nil now — still a no-op
	b.Broadcast()

	select {
	case <-ch:
	default:
		t.Error("live subscriber missed the broadcast after an unknown Unsubscribe")
	}
}

// ── ServeHTTP ──────────────────────────────────────────────────────────────

// nonFlusherWriter is an http.ResponseWriter that deliberately does not
// implement http.Flusher, so ServeHTTP takes its unsupported-streaming branch.
type nonFlusherWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *nonFlusherWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *nonFlusherWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func (w *nonFlusherWriter) WriteHeader(status int) { w.status = status }

func TestSSEBroadcaster_ServeHTTP_NonFlushableWriterFailsWith500(t *testing.T) {
	var b SSEBroadcaster
	w := &nonFlusherWriter{}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events", nil)

	b.ServeHTTP(w, req)

	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.status, http.StatusInternalServerError)
	}
	if !strings.Contains(w.body.String(), "streaming not supported") {
		t.Errorf("body = %q, want it to explain that streaming is unsupported", w.body.String())
	}
}

func TestSSEBroadcaster_ServeHTTP_StreamsConnectedFrameThenReloadEvents(t *testing.T) {
	var b SSEBroadcaster
	srv := httptest.NewServer(&b)
	defer srv.Close()

	// The context deadline bounds every blocking read below: if the handler
	// stops producing frames the read fails instead of hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatalf("building SSE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening SSE stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", got, "text/event-stream")
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want %q", got, "no")
	}

	reader := bufio.NewReader(resp.Body)

	// The handler writes the connected frame only after Subscribe returns, so
	// receiving it proves this client is registered — no sleep needed before
	// the Broadcast below.
	if got := readSSEFrame(t, reader); got != ": connected\n" {
		t.Fatalf("first frame = %q, want the connected comment frame", got)
	}

	b.Broadcast()

	if got := readSSEFrame(t, reader); got != "event: reload\ndata: ok\n" {
		t.Errorf("second frame = %q, want a reload event", got)
	}

	// Cancelling the request context must end the handler's stream loop.
	cancel()
}

// readSSEFrame reads lines until the blank line that terminates an SSE frame
// and returns the frame body (blank terminator excluded).
func readSSEFrame(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var frame strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE frame (partial %q): %v", frame.String(), err)
		}
		if line == "\n" {
			return frame.String()
		}
		frame.WriteString(line)
	}
}
