package storage

import (
	"context"
	"sync"
)

// Notifier broadcasts notifications to multiple waiting goroutines.
// Unlike channels which only wake one receiver per send, this wakes all waiters.
type Notifier struct {
	mu         sync.Mutex
	cond       *sync.Cond
	generation uint64 // Incremented on each Notify to track notification state
}

// NewNotifier creates a new Notifier.
func NewNotifier() *Notifier {
	n := &Notifier{}
	n.cond = sync.NewCond(&n.mu)
	return n
}

// Wait blocks until notified or context cancellation.
// Returns true if notified, false if context canceled.
// Also returns true immediately if already notified (generation > 0).
func (n *Notifier) Wait(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}

	n.mu.Lock()
	// Fast path: if already notified, return immediately
	if n.generation > 0 {
		n.mu.Unlock()
		return true
	}

	startGen := n.generation
	ctxDone := false

	// Helper goroutine to handle context cancellation since
	// sync.Cond doesn't support context directly
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			n.mu.Lock()
			ctxDone = true
			n.cond.Signal() // Wake up the waiting goroutine
			n.mu.Unlock()
		case <-done:
			// Main goroutine exited, clean up
		}
	}()

	// Wait for either notification (generation change) or context cancellation
	for n.generation == startGen && !ctxDone {
		n.cond.Wait() // Releases lock while waiting
	}

	notified := n.generation != startGen
	n.mu.Unlock()
	close(done) // Signal helper goroutine to exit

	return notified
}

// Notify wakes all waiting goroutines by incrementing the generation
// counter and broadcasting on the condition variable.
func (n *Notifier) Notify() {
	n.mu.Lock()
	n.generation++
	n.cond.Broadcast() // Wake all waiting goroutines
	n.mu.Unlock()
}
