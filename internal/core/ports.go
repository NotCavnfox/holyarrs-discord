package core

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Errors adapters return so features can react without knowing the transport.
var (
	ErrNotLinked        = errors.New("not linked")    // acting for a user who has no link
	ErrForbidden        = errors.New("not permitted") // scope missing or route closed
	ErrNotFound         = errors.New("not found")
	ErrRateLimited      = errors.New("rate limited")
	ErrLimit            = errors.New("request limit reached")
	ErrAlreadyAvailable = errors.New("already in the library")
	ErrAlreadyLinked    = errors.New("that HolyArrs user is linked to another Discord member")
	ErrUnsupported      = errors.New("this HolyArrs is too old for that") // the route does not exist
)

// HolyArrs is the port to the HolyArrs server. Implemented by
// internal/holyarrs; faked in tests.
type HolyArrs interface {
	// Users lists the names of people on the server.
	Users(ctx context.Context) ([]User, error)
	// NewLinkCode asks HolyArrs for a one-time code bound to an outside id
	// (the Discord user id). The person types it in My account.
	NewLinkCode(ctx context.Context, external string) (code string, expires time.Time, err error)
	// Links lists the links HolyArrs holds for this plugin, for users who can
	// still sign in. HolyArrs is the source of truth for who is linked.
	// Returns ErrUnsupported when HolyArrs has no such route.
	Links(ctx context.Context) ([]RemoteLink, error)
	// RemoveLink removes this plugin's link to user in HolyArrs. Removing a
	// link that is not there is not an error. Returns ErrUnsupported when
	// HolyArrs has no such route.
	RemoveLink(ctx context.Context, user UserID) error

	RecentlyAdded(ctx context.Context, limit int) ([]Title, error)
	Title(ctx context.Context, id TitleID) (Title, error)
	// Poster returns the stored poster bytes and their content type.
	Poster(ctx context.Context, id TitleID) (data []byte, contentType string, err error)
	// Artwork returns the stored picture a Title.Artwork names: at most 5 MB
	// of JPEG, PNG or WebP. ErrNotFound when there is no such picture,
	// ErrForbidden without the library.artwork.read permission.
	Artwork(ctx context.Context, ref string) (data []byte, contentType string, err error)
	// Thumbnail returns the same picture at HolyArrs's small size: a JPEG of
	// at most 250 pixels a side and at most 1 MB. Errors as for Artwork.
	Thumbnail(ctx context.Context, ref string) (data []byte, contentType string, err error)
	// WatchPeople returns per-person watch totals.
	WatchPeople(ctx context.Context) ([]PersonWatch, error)
	// RecentlyWatched returns titles by latest watch time, newest first.
	RecentlyWatched(ctx context.Context, limit int) ([]WatchedTitle, error)
	Upcoming(ctx context.Context, days int) ([]CalendarItem, error)

	// AskedBy returns who asked for each library title. HolyArrs gives a
	// plugin no list of requests; this lookup and CreateRequest's own
	// result are the only request data there is.
	AskedBy(ctx context.Context, ids []TitleID) (map[TitleID][]UserID, error)
	Search(ctx context.Context, query string) ([]SearchResult, error)
	// CreateRequest makes a request as the linked user, under their own
	// limits and auto-approve rules. Returns ErrLimit over the person's
	// limit and ErrAlreadyAvailable when the title is already in the library.
	CreateRequest(ctx context.Context, as UserID, what SearchResult) (Request, error)

	// Events streams events until ctx ends. The channel closes on return.
	Events(ctx context.Context) (<-chan Event, error)
	// ServePages declares the pages and answers file requests until ctx ends.
	ServePages(ctx context.Context, pages []Page, serve PageHandler) error
	// SetHomeWidget publishes (or replaces) this plugin's card on the HolyArrs
	// Home page, which every signed-in user sees. HolyArrs keeps it in memory
	// until the plugin stops, so it is sent again on every start.
	SetHomeWidget(ctx context.Context, c Card) error
}

// Card is a Home widget: plain text that HolyArrs draws itself.
type Card struct {
	Title string
	Rows  []CardRow
}

// CardRow is one line of a Card. Sub is the smaller second line. Link is a
// page inside HolyArrs ("/settings/account"), never an outside URL.
type CardRow struct {
	Text, Sub, Link string
}

// Page is one plugin page shown under Plugins in HolyArrs.
type Page struct {
	ID    string
	Title string
	File  string
}

// PageHandler returns the bytes for a page file. ok false means 404.
type PageHandler func(file string) (body []byte, contentType string, ok bool)

// Discord is the port to Discord. Implemented by internal/discordgw.
// All ids are Discord snowflakes as text.
type Discord interface {
	// Open connects to the gateway, registers commands in the guild and
	// starts routing interactions to the router. Close disconnects.
	Open(ctx context.Context, cmds []Command, router InteractionHandler) error
	Close() error

	Send(ctx context.Context, channelID string, m Message) (messageID string, err error)
	Edit(ctx context.Context, channelID, messageID string, m Message) error
	Delete(ctx context.Context, channelID, messageID string) error
	Pin(ctx context.Context, channelID, messageID string) error
	// DM sends a private message. Returns ErrForbidden when the member has
	// DMs closed.
	DM(ctx context.Context, userID string, m Message) error

	AddRole(ctx context.Context, userID, roleID string) error
	RemoveRole(ctx context.Context, userID, roleID string) error

	// Respond answers an interaction within 3 seconds. Defer buys 15 minutes;
	// then use FollowUp. Update edits the message a button sits on.
	Respond(ctx context.Context, in *Interaction, m Message, ephemeral bool) error
	Defer(ctx context.Context, in *Interaction, ephemeral bool) error
	FollowUp(ctx context.Context, in *Interaction, m Message, ephemeral bool) error
	Update(ctx context.Context, in *Interaction, m Message) error
}

// Message is a Discord message in the bot's own terms.
type Message struct {
	Content string
	Embeds  []Embed
	Rows    [][]Button // up to 5 rows of up to 5 buttons
	Files   []File
}

type Embed struct {
	Title       string
	Description string
	URL         string
	Color       int
	Fields      []Field
	Thumbnail   string // https URL or "attachment://name"
	Image       string
	Footer      string
	Time        time.Time
}

type Field struct {
	Name   string
	Value  string
	Inline bool
}

type ButtonStyle int

const (
	ButtonPrimary ButtonStyle = iota
	ButtonSecondary
	ButtonSuccess
	ButtonDanger
	ButtonLink // uses URL, no CustomID
)

type Button struct {
	Label    string
	CustomID string // "<feature>:<action>[:<arg>]", at most 100 characters
	URL      string
	Style    ButtonStyle
	Disabled bool
}

type File struct {
	Name        string
	ContentType string
	Data        []byte
}

// OptionType is the type of a slash command option.
type OptionType int

const (
	OptString OptionType = iota
	OptInt
	OptBool
	OptUser
)

type Option struct {
	Name        string
	Description string
	Type        OptionType
	Required    bool
	Choices     []string
}

// Command is one slash command (or one sub-command when Sub is set on the
// parent). Handlers must answer through the Discord port.
type Command struct {
	Name        string
	Description string
	Options     []Option
	Sub         []Command // sub-commands; when set, Options and Handle are unused
	AdminOnly   bool
	Handle      func(ctx context.Context, in *Interaction) error
}

// Interaction is a slash command call or a button press.
type Interaction struct {
	ID        string
	Token     string
	GuildID   string
	ChannelID string
	MessageID string // set for button presses
	UserID    string // Discord id of the caller
	UserName  string
	IsAdmin   bool // has the admin role or the Manage Server permission

	Command  string            // "request", or "purge-vote start" for a sub-command
	Options  map[string]string // values as text
	CustomID string            // set for button presses
}

// InteractionHandler receives every interaction the adapter cannot route to a
// Command by name, that is, button presses.
type InteractionHandler func(ctx context.Context, in *Interaction) error

// Store persists small JSON documents in the plugin data folder.
type Store interface {
	// Load fills v from <name>.json. A missing file leaves v as it is.
	Load(name string, v any) error
	// Save writes v to <name>.json atomically.
	Save(name string, v any) error
}

// Links is the shared book of Discord-to-HolyArrs links.
type Links interface {
	ByDiscord(discordID string) (Link, bool)
	ByUser(user UserID) (Link, bool)
	All() []Link
	Put(l Link) error
	Remove(discordID string) error
}

// Events hands each subscriber its own buffered channel of events. A slow
// subscriber drops events instead of blocking the others.
type Events interface {
	Subscribe(name string) <-chan Event
}

// Deps is what every feature receives.
type Deps struct {
	Cfg    Config
	HA     HolyArrs
	DC     Discord
	Store  Store
	Links  Links
	Events Events
	Log    *slog.Logger
	Now    func() time.Time
}

// Feature is one self-contained slice of the bot. Adding a feature means
// adding a package and one line in main; nothing else changes.
type Feature interface {
	Name() string
	// Commands returns the slash commands this feature owns.
	Commands() []Command
	// Run does the feature's background work and returns when ctx ends.
	// Features with no background work block on ctx.Done.
	Run(ctx context.Context) error
}

// ComponentFeature is implemented by features that own buttons. The app
// routes a press to the feature whose Name is the CustomID prefix before ":".
type ComponentFeature interface {
	Feature
	HandleComponent(ctx context.Context, in *Interaction) error
}
