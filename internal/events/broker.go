package events

import (
	"sync"
)

// Broker manages SSE (Server-Sent Events) subscriber channels.
// Thread-safe. Clients subscribe by providing a channel; the broker
// fans out every CloudEvent to all active subscribers.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[chan CloudEvent]struct{}
}

// NewBroker creates an empty Broker.
func NewBroker() *Broker {
	return &Broker{
		subscribers: make(map[chan CloudEvent]struct{}),
	}
}

// Subscribe registers a channel and returns it. Events will be sent
// to the channel until Unsubscribe is called or the broker is closed.
// The channel should be buffered (e.g., 64) to avoid blocking the broker.
func (b *Broker) Subscribe(ch chan CloudEvent) chan CloudEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[ch] = struct{}{}
	return ch
}

// Unsubscribe removes a channel from the subscriber set.
// The caller should close the channel after Unsubscribe returns.
// Broker no longer closes the channel itself — that way Publish
// never attempts a send on a closed channel (the close happens
// after the channel is removed from the subscriber map, under
// the write lock).
func (b *Broker) Unsubscribe(ch chan CloudEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
}

// Publish sends an event to all active subscribers. Non-blocking per
// subscriber — if a subscriber's buffer is full, the event is dropped
// for that subscriber (no slow consumer backpressure).
func (b *Broker) Publish(evt CloudEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- evt:
		default:
			// drop for slow consumer
		}
	}
}

// Len returns the number of active subscribers.
func (b *Broker) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
