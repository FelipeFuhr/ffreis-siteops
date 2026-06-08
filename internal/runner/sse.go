package runner

import (
	"fmt"
	"net/http"
	"sync"
)

// SSEBroadcaster fans out a single in-process Broadcast() call to all
// currently-subscribed HTTP clients via Server-Sent Events. Used by the dev
// command to push a "reload" event to every open browser tab after a
// compiler respawn.
//
// The zero value is usable; subs is lazily initialized inside Subscribe.
//
// Concurrency model:
//   - Subscribe / Unsubscribe / Broadcast hold the mutex briefly.
//   - Each subscriber gets a buffered channel of size 1. Broadcast does a
//     non-blocking send; if the buffer is full (the subscriber hasn't
//     consumed the previous event yet), the new event is dropped. This is
//     intentional: a slow tab should never stall the reload pipeline.
type SSEBroadcaster struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// Subscribe registers a new subscriber and returns its receive channel.
// The caller MUST call Unsubscribe when done to free the channel slot.
func (b *SSEBroadcaster) Subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[chan struct{}]struct{})
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber. Safe to call with a channel that's
// already been removed (no-op in that case).
func (b *SSEBroadcaster) Unsubscribe(ch chan struct{}) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// Broadcast notifies all current subscribers. Non-blocking: if a subscriber's
// buffer is full, the event is dropped for that subscriber only.
func (b *SSEBroadcaster) Broadcast() {
	b.mu.Lock()
	targets := make([]chan struct{}, 0, len(b.subs))
	for ch := range b.subs {
		targets = append(targets, ch)
	}
	b.mu.Unlock()
	for _, ch := range targets {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ServeHTTP serves a long-lived SSE stream. It sends an initial comment
// frame to confirm the connection, then forwards each broadcast as a
// `reload` event until the client disconnects.
//
// If the underlying ResponseWriter doesn't implement http.Flusher (e.g.
// during certain proxy chains), responds 500 — SSE is meaningless without
// flushing.
func (b *SSEBroadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	// Initial comment frame — clients use this to know the connection is up.
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			_, _ = fmt.Fprint(w, "event: reload\ndata: ok\n\n")
			flusher.Flush()
		}
	}
}
