package holyarrs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const askedByBatch = 200 // HolyArrs takes at most 200 title ids a call

// Users lists the people on the server: id and name only.
func (c *Client) Users(ctx context.Context) ([]core.User, error) {
	var out struct {
		Users []struct {
			ID   flexInt `json:"id"`
			Name string  `json:"name"`
		} `json:"users"`
	}
	if err := c.getJSON(ctx, "/api/v1/plugin-hooks/users", nil, &out); err != nil {
		return nil, err
	}
	users := make([]core.User, 0, len(out.Users))
	for _, u := range out.Users {
		if id := userIDOf(u.ID); id != "" {
			users = append(users, core.User{ID: id, Name: u.Name})
		}
	}
	return users, nil
}

// NewLinkCode asks for a one-time code bound to external (at most 100
// characters). A new call for the same external replaces the old code.
func (c *Client) NewLinkCode(ctx context.Context, external string) (string, time.Time, error) {
	cl := call{method: http.MethodPost, path: "/api/v1/plugin-hooks/link", body: map[string]string{"external": external}}
	rep, err := c.do(ctx, cl)
	if err != nil {
		return "", time.Time{}, err
	}
	var out struct {
		Code    string   `json:"code"`
		Expires flexTime `json:"expires"`
	}
	if err := c.decode(cl, rep, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.Code == "" {
		return "", time.Time{}, c.fail(cl, errors.New("the answer holds no code"))
	}
	return out.Code, out.Expires.Time, nil
}

const linksPath = "/api/v1/plugin-hooks/links"

// Links lists this plugin's links, ordered by user id. Entries without a
// user id are left out. An older HolyArrs has no such route and answers 404
// or 405: that comes back as core.ErrUnsupported.
func (c *Client) Links(ctx context.Context) ([]core.RemoteLink, error) {
	cl := call{method: http.MethodGet, path: linksPath}
	rep, err := c.do(ctx, cl)
	if err != nil {
		return nil, unsupported(err)
	}
	return c.decodeLinks(cl, rep)
}

// RemoveLink removes this plugin's link to user. HolyArrs answers 200 whether
// or not there was one. core.ErrUnsupported as for Links.
func (c *Client) RemoveLink(ctx context.Context, user core.UserID) error {
	if !validUser(user) {
		return fmt.Errorf("holyarrs: user id %q is not a number: %w", string(user), core.ErrNotFound)
	}
	cl := call{method: http.MethodDelete, path: linksPath + "/" + string(user)}
	rep, err := c.do(ctx, cl)
	if err != nil {
		return unsupported(err)
	}
	// Anything but the list of remaining links is not the route answering.
	_, err = c.decodeLinks(cl, rep)
	return err
}

// decodeLinks reads {"links":[...]}. An answer without the list is an error,
// never an empty list: callers drop local links that are missing from it.
func (c *Client) decodeLinks(cl call, rep *reply) ([]core.RemoteLink, error) {
	var out struct {
		Links *[]struct {
			UserID    flexInt  `json:"user_id"`
			External  flexStr  `json:"external"`
			CreatedAt flexTime `json:"created_at"`
		} `json:"links"`
	}
	if err := c.decode(cl, rep, &out); err != nil {
		return nil, err
	}
	if out.Links == nil {
		return nil, c.fail(cl, errors.New("the answer holds no list of links"))
	}
	links := make([]core.RemoteLink, 0, len(*out.Links))
	for _, l := range *out.Links {
		if id := userIDOf(l.UserID); id != "" {
			links = append(links, core.RemoteLink{UserID: id, External: string(l.External), CreatedAt: l.CreatedAt.Time})
		}
	}
	return links, nil
}

// unsupported turns the answer of a HolyArrs that does not offer this route to
// plugins into core.ErrUnsupported: 404 or 405 when the route is not there, or
// the 403 "plugins can't use this route" that HolyArrs 1.3.1 gives for a path
// under /plugin-hooks it does not know. Any other 403 (a missing permission)
// stays as it is. Use it only on routes that never answer 404 for any other
// reason.
func unsupported(err error) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err
	}
	switch {
	case ae.status == http.StatusNotFound, ae.status == http.StatusMethodNotAllowed:
		ae.kind = core.ErrUnsupported
	case ae.status == http.StatusForbidden && strings.Contains(string(ae.body)+ae.msg, notForPlugins):
		ae.kind = core.ErrUnsupported
	}
	return err
}

// notForPlugins is the text HolyArrs answers with when a plugin token calls a
// route that is not open to plugins at all.
const notForPlugins = "plugins can't use this route"

// AskedBy returns who asked for each title, earliest first. Titles nobody
// asked for are left out.
func (c *Client) AskedBy(ctx context.Context, ids []core.TitleID) (map[core.TitleID][]core.UserID, error) {
	out := map[core.TitleID][]core.UserID{}
	var valid []string
	seen := map[core.TitleID]bool{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			valid = append(valid, strconv.FormatInt(int64(id), 10))
		}
	}
	for len(valid) > 0 {
		n := min(len(valid), askedByBatch)
		var page struct {
			Titles map[string]struct {
				Askers []struct {
					ID flexInt `json:"id"`
				} `json:"askers"`
			} `json:"titles"`
		}
		if err := c.getJSON(ctx, "/api/v1/asked-by", url.Values{"title_id": valid[:n]}, &page); err != nil {
			return nil, err
		}
		valid = valid[n:]
		for k, e := range page.Titles {
			id, err := strconv.ParseInt(k, 10, 64)
			if err != nil {
				continue
			}
			for _, a := range e.Askers {
				if u := userIDOf(a.ID); u != "" {
					out[core.TitleID(id)] = append(out[core.TitleID(id)], u)
				}
			}
		}
	}
	return out, nil
}

// lookupWire is one result of the movie or show lookup.
type lookupWire struct {
	Name     string             `json:"name"`
	Year     flexInt            `json:"year"`
	Overview string             `json:"overview"`
	IDs      map[string]flexStr `json:"ids"`
	Poster   string             `json:"poster"`
	TitleID  flexInt            `json:"title_id"`
}

func (w lookupWire) result(kind core.Kind) (core.SearchResult, bool) {
	tmdb := string(w.IDs["tmdb"])
	if tmdb == "" || strings.Trim(tmdb, "0123456789") != "" {
		return core.SearchResult{}, false
	}
	return core.SearchResult{Ref: string(kind) + ":tmdb:" + tmdb, Name: w.Name, Year: int(w.Year), Kind: kind,
		Overview: w.Overview, PosterURL: publicURL(w.Poster), InLibrary: w.TitleID > 0}, true
}

// Search looks query up as a movie and as a show (TMDB) and merges the two
// lists, alternating so the best match of each kind comes first. Results
// HolyArrs has no TMDB id for are left out: they could not be requested.
func (c *Client) Search(ctx context.Context, query string) ([]core.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []core.SearchResult{}, nil
	}
	if len(query) > 200 { // the lookups refuse longer terms
		query = strings.ToValidUTF8(query[:200], "")
	}
	var (
		wg               sync.WaitGroup
		movies, shows    []lookupWire
		movieErr, showEr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		var out struct {
			Movies []lookupWire `json:"movies"`
		}
		movieErr = c.getJSON(ctx, "/api/v1/titles/lookup", url.Values{"term": {query}}, &out)
		movies = out.Movies
	}()
	go func() {
		defer wg.Done()
		var out struct {
			Shows []lookupWire `json:"shows"`
		}
		showEr = c.getJSON(ctx, "/api/v1/shows/lookup", url.Values{"term": {query}, "source": {"tmdb"}}, &out)
		shows = out.Shows
	}()
	wg.Wait()
	switch {
	case movieErr != nil && showEr != nil:
		return nil, movieErr
	case movieErr != nil:
		c.log.Warn("holyarrs: movie lookup failed, showing shows only", "err", movieErr)
	case showEr != nil:
		c.log.Warn("holyarrs: show lookup failed, showing movies only", "err", showEr)
	}
	out := make([]core.SearchResult, 0, len(movies)+len(shows))
	for i := 0; i < len(movies) || i < len(shows); i++ {
		if i < len(movies) {
			if r, ok := movies[i].result(core.KindMovie); ok {
				out = append(out, r)
			}
		}
		if i < len(shows) {
			if r, ok := shows[i].result(core.KindShow); ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// parseRef splits a SearchResult.Ref ("movie:tmdb:603") into the ask's kind
// and id.
func parseRef(ref string) (kind, id string, ok bool) {
	kind, id, found := strings.Cut(ref, ":")
	num, isTMDB := strings.CutPrefix(id, "tmdb:")
	if !found || !isTMDB || (kind != "movie" && kind != "show") || num == "" || len(num) > 12 || strings.Trim(num, "0123456789") != "" {
		return "", "", false
	}
	return kind, id, true
}

func mapAskState(s string) core.RequestStatus {
	switch s {
	case "asked":
		return core.RequestPending
	case "approved":
		return core.RequestApproved
	case "available":
		return core.RequestAvailable
	case "turned_down":
		return core.RequestDeclined
	}
	return core.RequestOther
}

// CreateRequest makes a request as the linked user. Joining a request that
// is already open succeeds and returns it.
func (c *Client) CreateRequest(ctx context.Context, as core.UserID, what core.SearchResult) (core.Request, error) {
	const path = "/api/v1/asks"
	if !validUser(as) {
		return core.Request{}, fmt.Errorf("holyarrs: POST %s: user id %q is not a number: %w", path, string(as), core.ErrNotLinked)
	}
	kind, id, ok := parseRef(what.Ref)
	if !ok {
		return core.Request{}, fmt.Errorf("holyarrs: POST %s: %q is not a search result reference", path, what.Ref)
	}
	cl := call{method: http.MethodPost, path: path, forUser: as, body: map[string]string{"id": id, "kind": kind}}
	rep, err := c.do(ctx, cl)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.status == http.StatusConflict && (hasField(ae.body, "title_id") || hasField(ae.body, "ready")) {
			ae.kind = core.ErrAlreadyAvailable
		}
		return core.Request{}, err
	}
	var out struct {
		Ask struct {
			ID      flexStr  `json:"id"`
			Kind    string   `json:"kind"`
			Name    string   `json:"name"`
			Year    flexInt  `json:"year"`
			Poster  string   `json:"poster"`
			State   string   `json:"state"`
			TitleID flexInt  `json:"title_id"`
			AskedAt flexTime `json:"asked_at"`
			People  []struct {
				ID flexInt `json:"id"`
			} `json:"people"`
		} `json:"ask"`
	}
	if err := c.decode(cl, rep, &out); err != nil {
		return core.Request{}, err
	}
	a := out.Ask
	req := core.Request{ID: string(a.ID), Status: mapAskState(a.State), CreatedAt: a.AskedAt.Time,
		Title: core.Title{ID: core.TitleID(a.TitleID), Name: a.Name, Year: int(a.Year), Kind: mapKind(a.Kind), PosterURL: publicURL(a.Poster)}}
	if req.Title.Name == "" {
		req.Title.Name, req.Title.Year = what.Name, what.Year
	}
	if a.Kind == "" {
		req.Title.Kind = what.Kind
	}
	for _, p := range a.People {
		if u := userIDOf(p.ID); u != "" {
			req.AskedBy = append(req.AskedBy, u)
		}
	}
	if len(req.AskedBy) == 0 {
		req.AskedBy = []core.UserID{as}
	}
	return req, nil
}
