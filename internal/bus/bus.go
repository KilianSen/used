// Package bus fans events out to subscribers (webhooks, API streams).
// Slow subscribers lose events rather than stall the daemon; `seq` in each
// payload lets receivers notice gaps.
package bus

import (
	"slices"
	"sync"
)

// Msg is an already-encoded event.
type Msg struct {
	Type string
	ID   string
	Body []byte
}

type Bus struct {
	mu   sync.Mutex
	subs map[chan Msg]struct{}
}

func New() *Bus { return &Bus{subs: map[chan Msg]struct{}{}} }

// Subscribe returns a channel of events and a function to stop receiving.
func (b *Bus) Subscribe(buffer int) (<-chan Msg, func()) {
	ch := make(chan Msg, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

func (b *Bus) Publish(m Msg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- m:
		default: // full: drop for this subscriber
		}
	}
}

// Wants reports whether an event filter (empty = everything) accepts typ.
func Wants(filter []string, typ string) bool {
	return len(filter) == 0 || slices.Contains(filter, typ)
}
