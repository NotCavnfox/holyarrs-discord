package holyarrs

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// The flex types decode defensively: a field that is missing, null or of an
// unexpected type becomes the zero value instead of failing the whole answer.

// flexInt is a whole number sent as a number or as text.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	*f = 0
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(n)
	} else if x, err := strconv.ParseFloat(s, 64); err == nil && x > -1e18 && x < 1e18 {
		*f = flexInt(x)
	}
	return nil
}

// flexStr is text sent as text or as a number.
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	*f = ""
	s := strings.TrimSpace(string(b))
	switch {
	case s == "null" || s == "":
	case s[0] == '"':
		if u, err := strconv.Unquote(s); err == nil {
			*f = flexStr(u)
		}
	case s[0] == '{' || s[0] == '[' || s == "true" || s == "false":
	default:
		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			// A whole number is kept digit for digit: ids are longer than a float holds.
		} else if x, err := strconv.ParseFloat(s, 64); err == nil && x == float64(int64(x)) {
			s = strconv.FormatInt(int64(x), 10)
		}
		*f = flexStr(s)
	}
	return nil
}

// flexTime is a time in RFC 3339 (HolyArrs also uses nine fixed fraction
// digits, which parses the same) or a plain day.
type flexTime struct{ time.Time }

func (f *flexTime) UnmarshalJSON(b []byte) error {
	f.Time = parseTime(strings.Trim(strings.TrimSpace(string(b)), `"`))
	return nil
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// mapKind turns a HolyArrs title kind into a core kind. Music titles are
// artists in HolyArrs; the bot shows them as albums.
func mapKind(s string) core.Kind {
	switch s {
	case "movie":
		return core.KindMovie
	case "show":
		return core.KindShow
	case "artist", "album":
		return core.KindAlbum
	}
	return core.KindOther
}

// publicURL keeps s only when Discord could fetch it by itself: an https
// link with a host. HolyArrs' own /api/v1/artwork/... paths are not public.
func publicURL(s string) string {
	if !strings.HasPrefix(s, "https://") {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	return s
}

func firstPublic(list []string) string {
	for _, s := range list {
		if u := publicURL(s); u != "" {
			return u
		}
	}
	return ""
}

func userIDOf(n flexInt) core.UserID {
	if n <= 0 {
		return ""
	}
	return core.UserID(strconv.FormatInt(int64(n), 10))
}

// validUser says whether id can go in X-Holyarrs-For-User: a positive number.
func validUser(id core.UserID) bool {
	n, err := strconv.ParseInt(string(id), 10, 64)
	return err == nil && n > 0
}

func titlePath(id core.TitleID) string {
	return "/api/v1/titles/" + strconv.FormatInt(int64(id), 10)
}
