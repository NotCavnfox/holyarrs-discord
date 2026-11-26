// Package links implements core.Links: the book of Discord-to-HolyArrs links,
// kept in memory, indexed both ways, and saved to the store on every change.
package links

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// Document is the store document name (links.json).
const Document = "links"

// ErrUserLinked is returned by Put when the HolyArrs user already belongs to
// another Discord member. It wraps core.ErrAlreadyLinked.
var ErrUserLinked = fmt.Errorf("links: %w", core.ErrAlreadyLinked)

type record struct {
	DiscordID    string `json:"discord_id"`
	UserID       string `json:"user_id"`
	UserName     string `json:"user_name"`
	ShowWatching bool   `json:"show_watching"`
	// AlertsOff is left out when false, so a file written before the field
	// existed reads as alerts on.
	AlertsOff bool      `json:"alerts_off,omitempty"`
	LinkedAt  time.Time `json:"linked_at"`
}

type document struct {
	Version int      `json:"version"`
	Links   []record `json:"links"`
}

// Book is the link book. It is safe for concurrent use.
type Book struct {
	st core.Store

	mu        sync.RWMutex
	byDiscord map[string]core.Link
	byUser    map[core.UserID]string // user id -> Discord id
}

// New loads the book from the store. A missing document is an empty book.
func New(st core.Store) (*Book, error) {
	if st == nil {
		return nil, errors.New("links: nil store")
	}
	var doc document
	if err := st.Load(Document, &doc); err != nil {
		return nil, fmt.Errorf("links: load: %w", err)
	}
	b := &Book{
		st:        st,
		byDiscord: make(map[string]core.Link, len(doc.Links)),
		byUser:    make(map[core.UserID]string, len(doc.Links)),
	}
	for i, r := range doc.Links {
		l := core.Link{
			DiscordID:    r.DiscordID,
			UserID:       core.UserID(r.UserID),
			UserName:     r.UserName,
			ShowWatching: r.ShowWatching,
			AlertsOff:    r.AlertsOff,
			LinkedAt:     r.LinkedAt,
		}
		if l.DiscordID == "" || l.UserID == "" {
			return nil, fmt.Errorf("links: entry %d in %s.json has an empty id", i, Document)
		}
		if _, dup := b.byDiscord[l.DiscordID]; dup {
			return nil, fmt.Errorf("links: Discord id %s appears twice in %s.json", l.DiscordID, Document)
		}
		if _, dup := b.byUser[l.UserID]; dup {
			return nil, fmt.Errorf("links: user %s appears twice in %s.json", l.UserID, Document)
		}
		b.byDiscord[l.DiscordID] = l
		b.byUser[l.UserID] = l.DiscordID
	}
	return b, nil
}

// ByDiscord returns the link for a Discord member.
func (b *Book) ByDiscord(discordID string) (core.Link, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	l, ok := b.byDiscord[discordID]
	return l, ok
}

// ByUser returns the link for a HolyArrs user.
func (b *Book) ByUser(user core.UserID) (core.Link, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	id, ok := b.byUser[user]
	if !ok {
		return core.Link{}, false
	}
	return b.byDiscord[id], true
}

// All returns every link, ordered by Discord id.
func (b *Book) All() []core.Link {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sorted(b.byDiscord)
}

// Put adds l, replacing any entry for the same Discord id. It fails with
// ErrUserLinked (errors.Is core.ErrAlreadyLinked) when the HolyArrs user
// belongs to a different Discord id.
// Nothing changes in memory unless the save succeeds.
func (b *Book) Put(l core.Link) error {
	if l.DiscordID == "" || l.UserID == "" {
		return errors.New("links: link needs a Discord id and a user id")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if owner, ok := b.byUser[l.UserID]; ok && owner != l.DiscordID {
		return ErrUserLinked
	}
	next := make(map[string]core.Link, len(b.byDiscord)+1)
	for k, v := range b.byDiscord {
		next[k] = v
	}
	next[l.DiscordID] = l
	if err := b.save(next); err != nil {
		return err
	}
	if old, ok := b.byDiscord[l.DiscordID]; ok {
		delete(b.byUser, old.UserID)
	}
	b.byDiscord = next
	b.byUser[l.UserID] = l.DiscordID
	return nil
}

// Remove deletes the link for a Discord member. Removing a member with no
// link is not an error.
func (b *Book) Remove(discordID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.byDiscord[discordID]
	if !ok {
		return nil
	}
	next := make(map[string]core.Link, len(b.byDiscord))
	for k, v := range b.byDiscord {
		if k != discordID {
			next[k] = v
		}
	}
	if err := b.save(next); err != nil {
		return err
	}
	b.byDiscord = next
	delete(b.byUser, old.UserID)
	return nil
}

func (b *Book) sorted(m map[string]core.Link) []core.Link {
	out := make([]core.Link, 0, len(m))
	for _, l := range m {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DiscordID < out[j].DiscordID })
	return out
}

func (b *Book) save(m map[string]core.Link) error {
	doc := document{Version: 1, Links: make([]record, 0, len(m))}
	for _, l := range b.sorted(m) {
		doc.Links = append(doc.Links, record{
			DiscordID:    l.DiscordID,
			UserID:       string(l.UserID),
			UserName:     l.UserName,
			ShowWatching: l.ShowWatching,
			AlertsOff:    l.AlertsOff,
			LinkedAt:     l.LinkedAt,
		})
	}
	if err := b.st.Save(Document, doc); err != nil {
		return fmt.Errorf("links: save: %w", err)
	}
	return nil
}
