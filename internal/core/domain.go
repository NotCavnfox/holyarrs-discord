// Package core holds the domain types and the ports (interfaces) every other
// package is written against. It imports nothing outside the standard library.
//
// Dependency rule: features -> core <- adapters. Features never import the
// Discord library or know the HolyArrs wire format; adapters never import
// features. Only main wires concrete types together.
package core

import "time"

// UserID is a HolyArrs user id, kept as text so the wire type can change.
type UserID string

// TitleID is a HolyArrs library title id.
type TitleID int64

// Kind is the sort of a title.
type Kind string

const (
	KindMovie Kind = "movie"
	KindShow  Kind = "show"
	KindAlbum Kind = "album"
	KindOther Kind = "other"
)

// User is a person on the HolyArrs server. Plugins only ever see the name.
type User struct {
	ID   UserID
	Name string
}

// Title is one library title, trimmed to what the bot shows.
type Title struct {
	ID       TitleID
	Name     string
	Year     int
	Kind     Kind
	Overview string
	AddedAt  time.Time // zero when unknown
	// PosterURL is a public https image link (for example from the metadata
	// source) that Discord can fetch by itself. Empty when there is none; then
	// use HolyArrs.Poster and upload the bytes.
	PosterURL string
	// Artwork names the poster HolyArrs has stored for the title, for
	// HolyArrs.Artwork. Empty when it has none. It is opaque and not a URL
	// Discord could fetch.
	Artwork string
}

// PersonWatch is the polled watch total for one media-server account.
// HolyArrs keeps totals per person, not which person watched which title.
type PersonWatch struct {
	UserID      UserID // empty when the media-server account has no HolyArrs user
	UserName    string // HolyArrs name when linked there, else the media-server name
	Movies      int
	Shows       int
	Episodes    int
	Plays       int
	LastWatched time.Time // zero when unknown
}

// WatchedTitle is a title someone watched, with the latest time anyone did.
// It never says who.
type WatchedTitle struct {
	Title       Title
	LastWatched time.Time
}

// CalendarItem is one upcoming release.
type CalendarItem struct {
	Title Title
	Label string // for example "S02E05 · The Bridge"; empty for movies
	Date  time.Time
}

// RequestStatus is the state of a request.
type RequestStatus string

const (
	RequestPending   RequestStatus = "pending"
	RequestApproved  RequestStatus = "approved"
	RequestAvailable RequestStatus = "available"
	RequestDeclined  RequestStatus = "declined"
	RequestOther     RequestStatus = "other"
)

// Request is one request with the people who asked for it.
type Request struct {
	ID        string
	Title     Title // ID is zero until the title is in the library
	Status    RequestStatus
	AskedBy   []UserID
	CreatedAt time.Time
}

// SearchResult is a title found by name. Ref is opaque: hand the whole value
// back to HolyArrs.CreateRequest.
type SearchResult struct {
	Ref       string
	Name      string
	Year      int
	Kind      Kind
	Overview  string
	PosterURL string
	InLibrary bool
}

// EventType names the events features react to. Adapters map wire names onto
// these; anything else arrives as EventOther with its WireType. The link
// events are the bot's own: the link book publishes them (see main).
type EventType string

const (
	EventTitleAdded       EventType = "title.added"
	EventRequestCreated   EventType = "request.created"
	EventRequestApproved  EventType = "request.approved"
	EventRequestAvailable EventType = "request.available"
	EventRequestDeclined  EventType = "request.declined"
	EventOther            EventType = "other"
	// EventLinkChanged: a link was added or its choices changed.
	// EventLinkRemoved: a member's link is gone. Both carry Member only.
	EventLinkChanged EventType = "link.changed"
	EventLinkRemoved EventType = "link.removed"
)

// Event is something that happened in HolyArrs, or to a link. Events carry
// ids only; look the rest up through the HolyArrs port or the link book.
type Event struct {
	Type      EventType
	WireType  string
	TitleID   TitleID
	RequestID string
	At        time.Time
	Member    string // Discord id; set on link events only
}

// RemoteLink is one link as HolyArrs holds it for this plugin. External is the
// text the plugin gave when it asked for the link code: the Discord user id.
type RemoteLink struct {
	UserID    UserID
	External  string
	CreatedAt time.Time // zero when unknown
}

// Link ties one Discord member to one HolyArrs user.
type Link struct {
	DiscordID    string
	UserID       UserID
	UserName     string
	ShowWatching bool // opt-in: show this person's name next to what they watch
	// AlertsOff is the opt-out from the DM sent when a title the person asked
	// for is available. The zero value means alerts are on.
	AlertsOff bool
	LinkedAt  time.Time
}

// Config is everything the owner sets, read once at start. Saving settings
// restarts the plugin, so it never changes while running.
type Config struct {
	BotToken         string
	GuildID          string
	InviteURL        string
	DashboardChannel string
	AnnounceChannel  string
	AdminRoleID      string
	LinkedRoleID     string
	RefreshEvery     time.Duration
	EnableDashboard  bool
	EnableAnnounce   bool
	EnableRequests   bool
	EnableHomeWidget bool
	DataDir          string
	HolyArrsTarget   string
	HolyArrsToken    string
}
