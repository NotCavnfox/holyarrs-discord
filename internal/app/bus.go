// Package app wires the running bot together: the event bus, the interaction
// router and the feature runner.
package app

import (
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// SubscriberBuffer is the size of each subscriber's channel.
const SubscriberBuffer = 256

// DefaultPublishWait is how long Publish waits for room in a full subscriber
// before it drops the event.
const DefaultPublishWait = 2 * time.Second

// Bus fans events out to subscribers. It implements core.Events. Publish
// waits a bounded time for a full subscriber and then drops; a subscriber
// that already timed out loses further events at once until it has room
// again, so one stuck feature cannot hold up the others for long.
type Bus struct {
	log *slog.Logger
	// PublishWait overrides DefaultPublishWait. Set it before publishing.
	PublishWait time.Duration

	pubMu sync.Mutex // one Publish at a time; Close waits for it

	mu     sync.Mutex
	subs   map[string]*subscriber
	closed bool
	done   chan struct{} // closed by Close; ends a waiting Publish
}

type subscriber struct {
	name    string
	ch      chan core.Event
	closed  bool // ch is closed. Guarded by Bus.mu.
	dropped int  // events lost in the current burst; 0 when keeping up. Guarded by pubMu.
}

// NewBus returns an empty bus. A nil log discards.
func NewBus(log *slog.Logger) *Bus {
	return &Bus{
		log: orDiscard(log), PublishWait: DefaultPublishWait,
		subs: make(map[string]*subscriber), done: make(chan struct{}),
	}
}

// Subscribe returns the channel for name, making it on first use. Asking
// again with the same name returns the same channel. After Close the channel
// comes back already closed.
func (b *Bus) Subscribe(name string) <-chan core.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.subs[name]; ok {
		return s.ch
	}
	s := &subscriber{name: name, ch: make(chan core.Event, SubscriberBuffer)}
	if b.closed {
		close(s.ch)
		s.closed = true
	}
	b.subs[name] = s
	return s.ch
}

// Publish hands e to every subscriber. A subscriber whose buffer is full is
// given up to PublishWait to make room; after that the event is dropped for
// it, which is logged once per burst and once more with the count when the
// subscriber catches up. While a subscriber is in such a burst, further
// events are dropped for it without waiting. Publish never blocks forever.
func (b *Bus) Publish(e core.Event) {
	b.pubMu.Lock()
	defer b.pubMu.Unlock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	subs := make([]*subscriber, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	// Channels are closed only by Close, which takes pubMu first, so no send
	// below can hit a closed channel.
	for _, s := range subs {
		select {
		case s.ch <- e:
			if s.dropped > 0 {
				b.log.Info("event subscriber caught up", "subscriber", s.name, "dropped", s.dropped)
				s.dropped = 0
			}
			continue
		default:
		}
		if s.dropped == 0 && b.waitSend(s, e) {
			continue
		}
		if s.dropped == 0 {
			b.log.Warn("event subscriber is full, dropping events", "subscriber", s.name, "event", string(e.Type))
		}
		s.dropped++
	}
}

// waitSend blocks until s takes e, the wait runs out or the bus closes.
func (b *Bus) waitSend(s *subscriber, e core.Event) bool {
	wait := b.PublishWait
	if wait <= 0 {
		return false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case s.ch <- e:
		return true
	case <-t.C:
		return false
	case <-b.done:
		return false
	}
}

// Close closes every subscriber channel. Later Publish calls do nothing.
// Safe to call more than once.
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.done)
	b.mu.Unlock()

	b.pubMu.Lock() // a Publish in flight has seen done and is leaving
	defer b.pubMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		if !s.closed {
			close(s.ch)
			s.closed = true
		}
	}
}

func orDiscard(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return log
}
