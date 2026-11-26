package holyarrs

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	widgetPath = "/api/v1/plugin-hooks/home-widget"
	// HolyArrs' own limits for a card; one over either and it refuses the lot.
	cardTextMax = 200 // characters in a title, text or sub
	cardRowsMax = 20
)

// cardLinkRe is HolyArrs' rule for a row link: a page inside the app.
var cardLinkRe = regexp.MustCompile(`^/[a-z0-9][a-z0-9/_\-?=&.]*$`)

// SetHomeWidget publishes c as this plugin's card on Home, replacing the one
// before. HolyArrs keeps it in memory until the plugin stops and has no route
// to remove it. The card is fitted to HolyArrs' rules first: "<" and ">" are
// taken out, texts cut to 200 characters, rows without text dropped, at most
// 20 rows, and a link that is not a HolyArrs page is left off.
func (c *Client) SetHomeWidget(ctx context.Context, card core.Card) error {
	type rowWire struct {
		Text string `json:"text"`
		Sub  string `json:"sub,omitempty"`
		Link string `json:"link,omitempty"`
	}
	body := struct {
		Title string    `json:"title"`
		Rows  []rowWire `json:"rows"`
	}{Title: cardText(card.Title), Rows: []rowWire{}}
	if body.Title == "" {
		return errors.New("holyarrs: home widget needs a title")
	}
	for _, r := range card.Rows {
		if len(body.Rows) == cardRowsMax {
			break
		}
		row := rowWire{Text: cardText(r.Text), Sub: cardText(r.Sub)}
		if row.Text == "" {
			continue
		}
		if cardLinkOK(r.Link) {
			row.Link = r.Link
		}
		body.Rows = append(body.Rows, row)
	}
	_, err := c.do(ctx, call{method: http.MethodPut, path: widgetPath, body: body})
	return err
}

// cardText makes s plain text HolyArrs accepts.
func cardText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '<' || r == '>' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > cardTextMax {
		s = strings.TrimSpace(string([]rune(s)[:cardTextMax]))
	}
	return s
}

func cardLinkOK(l string) bool {
	return cardLinkRe.MatchString(l) && !strings.Contains(l, "..") && l != "/api" && !strings.HasPrefix(l, "/api/")
}
