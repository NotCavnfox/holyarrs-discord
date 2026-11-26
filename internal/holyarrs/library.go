package holyarrs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	artworkPrefix  = "/api/v1/artwork/"
	maxArtwork     = 5 << 20 // largest picture read
	artworkTimeout = 15 * time.Second
	// maxWatchPeople bounds the per-account reads RecentlyWatched makes.
	maxWatchPeople = 20
)

// titleWire is the part of GET /api/v1/titles/{id} the bot shows.
type titleWire struct {
	ID       flexInt  `json:"id"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Year     flexInt  `json:"year"`
	Overview *string  `json:"overview"`
	Artwork  []string `json:"artwork"`
	AddedAt  flexTime `json:"added_at"`
}

func (w titleWire) title() core.Title {
	t := core.Title{ID: core.TitleID(w.ID), Name: w.Name, Year: int(w.Year), Kind: mapKind(w.Kind),
		AddedAt: w.AddedAt.Time, PosterURL: firstPublic(w.Artwork), Artwork: firstStored(w.Artwork)}
	if w.Overview != nil {
		t.Overview = *w.Overview
	}
	return t
}

// homeCard is one card of a /home row.
type homeCard struct {
	Kind    string   `json:"kind"`
	TitleID flexInt  `json:"title_id"`
	Name    string   `json:"name"`
	Year    flexInt  `json:"year"`
	Poster  string   `json:"poster"`
	At      flexTime `json:"at"`
}

// RecentlyAdded lists the titles that got new files lately, newest first.
// It reads Home's "Just arrived" row: at most 20 titles from the last two
// weeks; AddedAt is when the newest file arrived.
func (c *Client) RecentlyAdded(ctx context.Context, limit int) ([]core.Title, error) {
	var out struct {
		Arrived []homeCard `json:"arrived"`
	}
	if err := c.getJSON(ctx, "/api/v1/home", nil, &out); err != nil {
		return nil, err
	}
	titles := make([]core.Title, 0, len(out.Arrived))
	for _, a := range out.Arrived {
		if a.TitleID <= 0 {
			continue
		}
		if limit > 0 && len(titles) == limit {
			break
		}
		titles = append(titles, core.Title{ID: core.TitleID(a.TitleID), Name: a.Name, Year: int(a.Year),
			Kind: mapKind(a.Kind), AddedAt: a.At.Time, PosterURL: publicURL(a.Poster), Artwork: storedArtwork(a.Poster)})
	}
	return titles, nil
}

func (c *Client) titleWire(ctx context.Context, id core.TitleID) (titleWire, error) {
	var w titleWire
	err := c.getJSON(ctx, titlePath(id), nil, &w)
	if err == nil && w.ID == 0 {
		w.ID = flexInt(id)
	}
	return w, err
}

// Title reads one library title.
func (c *Client) Title(ctx context.Context, id core.TitleID) (core.Title, error) {
	w, err := c.titleWire(ctx, id)
	if err != nil {
		return core.Title{}, err
	}
	return w.title(), nil
}

// Poster returns the stored poster of a title: its first artwork entry.
// A title with no stored picture gives core.ErrNotFound.
func (c *Client) Poster(ctx context.Context, id core.TitleID) ([]byte, string, error) {
	w, err := c.titleWire(ctx, id)
	if err != nil {
		return nil, "", err
	}
	entry := firstStored(w.Artwork)
	if entry == "" {
		return nil, "", fmt.Errorf("holyarrs: poster of title %d: %w", id, core.ErrNotFound)
	}
	return c.Artwork(ctx, entry)
}

// Artwork returns one stored picture by the path HolyArrs lists it under
// (core.Title.Artwork), at full size: a stored file, so HolyArrs fetches
// nothing for it. It needs the library.artwork.read permission. Anything
// that is not such a path gives core.ErrNotFound without a call.
func (c *Client) Artwork(ctx context.Context, ref string) ([]byte, string, error) {
	if storedArtwork(ref) == "" {
		return nil, "", fmt.Errorf("holyarrs: artwork: not a stored picture: %w", core.ErrNotFound)
	}
	rep, err := c.do(ctx, call{method: http.MethodGet, path: ref, limit: maxArtwork, timeout: artworkTimeout})
	if err != nil {
		return nil, "", err
	}
	ct := rep.header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(rep.body)
	}
	return rep.body, ct, nil
}

// WatchPeople returns the watch totals per media-server account. With watch
// history switched off in HolyArrs the list is empty.
func (c *Client) WatchPeople(ctx context.Context) ([]core.PersonWatch, error) {
	var out struct {
		Enabled bool `json:"enabled"`
		People  []struct {
			Name          string   `json:"name"`
			Linked        bool     `json:"linked"`
			UserID        flexInt  `json:"user_id"`
			UserName      string   `json:"user_name"`
			Movies        flexInt  `json:"movies"`
			Shows         flexInt  `json:"shows"`
			Episodes      flexInt  `json:"episodes"`
			Plays         flexInt  `json:"plays"`
			LastWatchedAt flexTime `json:"last_watched_at"`
		} `json:"people"`
	}
	if err := c.getJSON(ctx, "/api/v1/library/stats/watched", nil, &out); err != nil {
		return nil, err
	}
	people := []core.PersonWatch{}
	if !out.Enabled {
		return people, nil
	}
	for _, p := range out.People {
		pw := core.PersonWatch{UserName: p.Name, Movies: int(p.Movies), Shows: int(p.Shows),
			Episodes: int(p.Episodes), Plays: int(p.Plays), LastWatched: p.LastWatchedAt.Time}
		if p.Linked {
			pw.UserID = userIDOf(p.UserID)
			if p.UserName != "" {
				pw.UserName = p.UserName
			}
		}
		people = append(people, pw)
	}
	return people, nil
}

// cleanupWire is GET /api/v1/library/watched-cleanup.
type cleanupWire struct {
	Enabled bool `json:"enabled"`
	People  []struct {
		Key string `json:"key"`
	} `json:"people"`
	Found []struct {
		ID            flexInt  `json:"id"`
		Kind          string   `json:"kind"`
		Name          string   `json:"name"`
		Year          flexInt  `json:"year"`
		Bytes         flexInt  `json:"bytes"`
		LastWatchedAt flexTime `json:"last_watched_at"`
	} `json:"found"`
}

func (c *Client) cleanup(ctx context.Context, q url.Values) (cleanupWire, error) {
	var out cleanupWire
	err := c.getJSON(ctx, "/api/v1/library/watched-cleanup", q, &out)
	return out, err
}

// RecentlyWatched returns titles by latest watch time, newest first.
//
// HolyArrs has no "watched lately" route for a plugin. The closest is the
// clean-up list, which with days=0 and one account lists every title that
// account finished and when. This asks once for the accounts, then once per
// account (at most maxWatchPeople), and keeps the latest time per title.
// Limits of that source: a show counts only when every episode with a file
// was watched, titles with a protected copy never show, and a requested
// title shows only after everyone who asked finished it.
func (c *Client) RecentlyWatched(ctx context.Context, limit int) ([]core.WatchedTitle, error) {
	first, err := c.cleanup(ctx, url.Values{"days": {"0"}})
	if err != nil {
		return nil, err
	}
	if !first.Enabled {
		return []core.WatchedTitle{}, nil
	}
	var keys []string
	for _, p := range first.People {
		if p.Key != "" && len(keys) < maxWatchPeople {
			keys = append(keys, p.Key)
		}
	}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
		best     = map[core.TitleID]core.WatchedTitle{}
	)
	add := func(w cleanupWire) {
		mu.Lock()
		defer mu.Unlock()
		for _, f := range w.Found {
			id := core.TitleID(f.ID)
			if id <= 0 {
				continue
			}
			if old, ok := best[id]; ok && !f.LastWatchedAt.After(old.LastWatched) {
				continue
			}
			best[id] = core.WatchedTitle{LastWatched: f.LastWatchedAt.Time,
				Title: core.Title{ID: id, Name: f.Name, Year: int(f.Year), Kind: mapKind(f.Kind)}}
		}
	}
	add(first) // what everyone finished; a subset of the per-account lists
	for _, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := c.cleanup(ctx, url.Values{"days": {"0"}, "people": {k}})
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			add(w)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	out := make([]core.WatchedTitle, 0, len(best))
	for _, w := range best {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastWatched.Equal(out[j].LastWatched) {
			return out[i].LastWatched.After(out[j].LastWatched)
		}
		return out[i].Title.ID < out[j].Title.ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Upcoming lists the calendar from today (UTC) for days days, soonest first.
func (c *Client) Upcoming(ctx context.Context, days int) ([]core.CalendarItem, error) {
	days = min(max(days, 1), 366)
	from := c.now().UTC().Truncate(24 * time.Hour)
	q := url.Values{"from": {from.Format("2006-01-02")}, "to": {from.AddDate(0, 0, days).Format("2006-01-02")}, "titles": {"all"}}
	var out struct {
		Dates []struct {
			TitleID     flexInt  `json:"title_id"`
			Name        string   `json:"name"`
			Year        flexInt  `json:"year"`
			Kind        string   `json:"kind"` // cinema, digital, physical, episode, album
			Date        flexTime `json:"date"`
			Season      *flexInt `json:"season"`
			Episode     *flexInt `json:"episode"`
			EpisodeName string   `json:"episode_name"`
			Album       string   `json:"album"`
			Poster      string   `json:"poster"`
		} `json:"dates"`
	}
	if err := c.getJSON(ctx, "/api/v1/calendar", q, &out); err != nil {
		return nil, err
	}
	items := make([]core.CalendarItem, 0, len(out.Dates))
	for _, d := range out.Dates {
		it := core.CalendarItem{Date: d.Date.Time, Title: core.Title{ID: core.TitleID(d.TitleID), Name: d.Name,
			Year: int(d.Year), Kind: core.KindMovie, PosterURL: publicURL(d.Poster), Artwork: storedArtwork(d.Poster)}}
		switch d.Kind {
		case "episode":
			it.Title.Kind = core.KindShow
			if d.Season != nil && d.Episode != nil {
				it.Label = fmt.Sprintf("S%02dE%02d", int64(*d.Season), int64(*d.Episode))
			}
			if d.EpisodeName != "" {
				if it.Label != "" {
					it.Label += " · "
				}
				it.Label += d.EpisodeName
			}
		case "album":
			it.Title.Kind = core.KindAlbum
			it.Label = d.Album
		case "cinema", "digital", "physical":
		default:
			it.Title.Kind = core.KindOther
		}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Date.Before(items[j].Date) })
	return items, nil
}

// CleanupCandidates lists the titles HolyArrs suggests could be deleted:
// finished by every watching account at least 30 days ago (its default).
func (c *Client) CleanupCandidates(ctx context.Context) ([]core.CleanupCandidate, error) {
	w, err := c.cleanup(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := []core.CleanupCandidate{}
	if !w.Enabled {
		return out, nil
	}
	var ids []core.TitleID
	for _, f := range w.Found {
		if f.ID <= 0 {
			continue
		}
		ids = append(ids, core.TitleID(f.ID))
		out = append(out, core.CleanupCandidate{SizeBytes: int64(f.Bytes), LastWatched: f.LastWatchedAt.Time,
			Title: core.Title{ID: core.TitleID(f.ID), Name: f.Name, Year: int(f.Year), Kind: mapKind(f.Kind)}})
	}
	asked, err := c.AskedBy(ctx, ids)
	if errors.Is(err, core.ErrForbidden) {
		// Without requests.read the list still stands, only without askers.
		c.log.Warn("holyarrs: clean-up list without askers", "err", err)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].AskedBy = asked[out[i].Title.ID]
	}
	return out, nil
}

// DeleteTitle removes a title and its files. It needs the library.delete
// scope; without it the answer is core.ErrForbidden.
func (c *Client) DeleteTitle(ctx context.Context, id core.TitleID) error {
	_, err := c.do(ctx, call{method: http.MethodPost, path: titlePath(id) + "/delete",
		body: map[string]any{"apply": true, "delete_files": true}})
	return err
}
