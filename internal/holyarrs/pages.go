package holyarrs

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const pagesPath = "/api/v1/plugin-hooks/pages"

// ServePages declares pages and answers HolyArrs' requests for their files
// until ctx ends (nil) or HolyArrs says the plugin was stopped (410, nil).
// The declaration lives in HolyArrs' memory only, so it is sent again after
// every error.
func (c *Client) ServePages(ctx context.Context, pages []core.Page, serve core.PageHandler) error {
	type pageWire struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		File  string `json:"file"`
	}
	decl := make([]pageWire, 0, len(pages))
	for _, p := range pages {
		decl = append(decl, pageWire{ID: p.ID, Title: p.Title, File: p.File})
	}
	declared := false
	delay := c.pagesMin
	// retry waits after err; false means stop serving.
	retry := func(err error) bool {
		if ctx.Err() != nil || statusOf(err) == http.StatusGone {
			return false
		}
		c.log.Warn("holyarrs: pages hook failed, retrying", "err", err, "wait", delay)
		if c.sleep(ctx, delay) != nil {
			return false
		}
		delay = min(delay*2, c.pagesMax)
		declared = false
		return true
	}
	for ctx.Err() == nil {
		if !declared {
			if _, err := c.do(ctx, call{method: http.MethodPut, path: pagesPath, body: decl}); err != nil {
				if !retry(err) {
					return nil
				}
				continue
			}
			declared = true
		}
		poll := call{method: http.MethodGet, path: pagesPath, timeout: c.pollTimeout}
		rep, err := c.do(ctx, poll)
		if err != nil {
			if !retry(err) {
				return nil
			}
			continue
		}
		if rep.status == http.StatusNoContent || len(rep.body) == 0 {
			delay = c.pagesMin
			continue
		}
		var job struct {
			ID      flexStr `json:"id"`
			Op      string  `json:"op"`
			Payload struct {
				Path string `json:"path"`
			} `json:"payload"`
		}
		if err := c.decode(poll, rep, &job); err != nil || job.ID == "" {
			if !retry(errors.Join(err, errors.New("holyarrs: pages job without an id"))) {
				return nil
			}
			continue
		}
		answer := map[string]any{"job": string(job.ID)}
		if job.Op == "file" {
			data := map[string]any{"status": http.StatusNotFound}
			if serve != nil {
				if body, ct, ok := serve(job.Payload.Path); ok {
					// encoding/json writes a byte slice as base64.
					data = map[string]any{"status": http.StatusOK, "type": ct, "body": body}
				}
			}
			answer["data"] = data
		} else {
			answer["error"] = fmt.Sprintf("this plugin does not know the %q job", job.Op)
		}
		_, err = c.do(ctx, call{method: http.MethodPost, path: pagesPath, body: answer})
		switch {
		case err == nil:
			delay = c.pagesMin
		case errors.Is(err, core.ErrNotFound):
			// The job ran out before the answer arrived; nothing to redo.
			c.log.Debug("holyarrs: pages answer came too late", "err", err)
		default:
			if !retry(err) {
				return nil
			}
		}
	}
	return nil
}
