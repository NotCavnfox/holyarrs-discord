// Package discordgw implements core.Discord on top of discordgo. It is the
// only package that imports discordgo.
//
// The HTTP client and the websocket dialer are discordgo's defaults, which
// read the standard proxy environment variables. Do not replace them with
// ones that ignore the environment: the plugin only reaches Discord through
// the sandbox proxy. No voice, no UDP.
package discordgw

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

const (
	// handlerTimeout stays inside the 15 minutes an interaction token lives.
	handlerTimeout = 14 * time.Minute
	failTimeout    = 10 * time.Second

	failText   = "Something went wrong."
	refuseText = "Only server admins can use this command."
)

var _ core.Discord = (*Gateway)(nil)

// Gateway is the Discord adapter.
type Gateway struct {
	token       string // bare token, kept only to scrub it from errors
	guildID     string
	adminRoleID string
	log         *slog.Logger
	s           *discordgo.Session

	mu       sync.Mutex
	opened   bool
	closed   bool
	appID    string
	base     context.Context
	cancel   context.CancelFunc
	routes   map[string]route
	router   core.InteractionHandler
	unhandle func()

	// notify tells the caller something failed; replaced in tests.
	notify func(in *core.Interaction, text string)
	wg     sync.WaitGroup
}

// New builds the adapter. It does not connect; Open does.
func New(token, guildID, adminRoleID string, log *slog.Logger) (*Gateway, error) {
	token = strings.TrimSpace(token)
	if rest, ok := strings.CutPrefix(token+" ", "Bot "); ok {
		token = strings.TrimSpace(rest)
	}
	if token == "" {
		return nil, errors.New("discord: bot token is empty")
	}
	if guildID == "" {
		return nil, errors.New("discord: guild id is empty")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, scrub(fmt.Errorf("discord: new session: %w", err), token)
	}
	// Slash commands and buttons arrive without any privileged intent.
	s.Identify.Intents = discordgo.IntentsGuilds
	g := &Gateway{token: token, guildID: guildID, adminRoleID: adminRoleID, log: log, s: s}
	g.notify = g.notifyFailure
	return g, nil
}

// Open connects, registers the guild commands and starts routing.
func (g *Gateway) Open(ctx context.Context, cmds []core.Command, router core.InteractionHandler) error {
	routes, err := buildRoutes(cmds)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	appCmds, err := toCommands(cmds, g.adminRoleID != "")
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errors.New("discord: gateway is closed")
	}
	if g.opened {
		return errors.New("discord: gateway is already open")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	g.base, g.cancel = context.WithCancel(ctx)
	g.routes = routes
	g.router = router
	g.unhandle = g.s.AddHandler(func(_ *discordgo.Session, ic *discordgo.InteractionCreate) {
		if ic != nil && ic.Interaction != nil {
			g.dispatch(ic.Interaction)
		}
	})

	fail := func(op string, err error) error {
		g.unhandle()
		g.cancel()
		_ = g.s.Close()
		return scrub(mapError(op, err), g.token)
	}
	if err := g.s.Open(); err != nil {
		return fail("connect", err)
	}
	appID := ""
	if g.s.State != nil && g.s.State.User != nil {
		appID = g.s.State.User.ID
	}
	if appID == "" {
		u, err := g.s.User("@me", discordgo.WithContext(ctx))
		if err != nil {
			return fail("read bot user", err)
		}
		appID = u.ID
	}
	g.appID = appID
	if _, err := g.s.ApplicationCommandBulkOverwrite(appID, g.guildID, appCmds, discordgo.WithContext(ctx)); err != nil {
		return fail("register commands", err)
	}
	g.opened = true
	g.log.Info("discord connected", "guild", g.guildID, "commands", len(appCmds))
	return nil
}

// Close stops routing and disconnects. Safe to call more than once and
// before Open.
func (g *Gateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	if g.cancel != nil {
		g.cancel()
	}
	if !g.opened {
		return nil
	}
	g.opened = false
	if g.unhandle != nil {
		g.unhandle()
	}
	if err := g.s.Close(); err != nil {
		return scrub(fmt.Errorf("discord close: %w", err), g.token)
	}
	return nil
}

// dispatch routes one interaction to its handler in a new goroutine.
func (g *Gateway) dispatch(i *discordgo.Interaction) {
	in, ok := toInteraction(i, g.adminRoleID)
	if !ok {
		return
	}
	g.mu.Lock()
	base, routes, router := g.base, g.routes, g.router
	g.mu.Unlock()
	if base == nil || base.Err() != nil {
		return
	}

	var handle func(context.Context, *core.Interaction) error
	name := in.Command
	if in.CustomID != "" {
		name = "component " + in.CustomID
		if router == nil {
			g.log.Warn("button press with no router", "custom_id", in.CustomID)
			return
		}
		handle = router
	} else {
		r, found := routes[in.Command]
		if !found {
			g.log.Warn("unknown command", "command", in.Command)
			g.notify(in, failText)
			return
		}
		if r.adminOnly && !in.IsAdmin {
			g.log.Info("admin command refused", "command", in.Command, "user", in.UserID)
			g.notify(in, refuseText)
			return
		}
		handle = r.handle
	}

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		ctx, cancel := context.WithTimeout(base, handlerTimeout)
		defer cancel()
		if err := safely(ctx, handle, in); err != nil {
			g.log.Error("interaction failed", "interaction", name, "user", in.UserID, "err", err)
			g.notify(in, failText)
		}
	}()
}

// safely runs a handler and turns a panic into an error.
func safely(ctx context.Context, h func(context.Context, *core.Interaction) error, in *core.Interaction) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return h(ctx, in)
}

// notifyFailure tells the caller privately. If the interaction was already
// answered or deferred, it falls back to a follow-up.
func (g *Gateway) notifyFailure(in *core.Interaction, text string) {
	g.mu.Lock()
	base := g.base
	g.mu.Unlock()
	if base == nil {
		return
	}
	// The handler's context may be the thing that expired, so use a new one.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(base), failTimeout)
	defer cancel()
	m := core.Message{Content: text}
	if err := g.Respond(ctx, in, m, true); err != nil {
		if err := g.FollowUp(ctx, in, m, true); err != nil {
			g.log.Warn("could not tell the caller about a failure", "user", in.UserID, "err", err)
		}
	}
}

func (g *Gateway) Send(ctx context.Context, channelID string, m core.Message) (string, error) {
	msg, err := g.s.ChannelMessageSendComplex(channelID, toSend(m), discordgo.WithContext(ctx))
	if err != nil {
		return "", mapError("send", err)
	}
	return msg.ID, nil
}

func (g *Gateway) Edit(ctx context.Context, channelID, messageID string, m core.Message) error {
	_, err := g.s.ChannelMessageEditComplex(toEdit(channelID, messageID, m), discordgo.WithContext(ctx))
	return mapError("edit", err)
}

func (g *Gateway) Delete(ctx context.Context, channelID, messageID string) error {
	return mapError("delete", g.s.ChannelMessageDelete(channelID, messageID, discordgo.WithContext(ctx)))
}

func (g *Gateway) Pin(ctx context.Context, channelID, messageID string) error {
	return mapError("pin", g.s.ChannelMessagePin(channelID, messageID, discordgo.WithContext(ctx)))
}

func (g *Gateway) DM(ctx context.Context, userID string, m core.Message) error {
	ch, err := g.s.UserChannelCreate(userID, discordgo.WithContext(ctx))
	if err != nil {
		return mapError("open dm", err)
	}
	_, err = g.s.ChannelMessageSendComplex(ch.ID, toSend(m), discordgo.WithContext(ctx))
	return mapError("dm", err)
}

func (g *Gateway) AddRole(ctx context.Context, userID, roleID string) error {
	return mapError("add role", g.s.GuildMemberRoleAdd(g.guildID, userID, roleID, discordgo.WithContext(ctx)))
}

func (g *Gateway) RemoveRole(ctx context.Context, userID, roleID string) error {
	return mapError("remove role", g.s.GuildMemberRoleRemove(g.guildID, userID, roleID, discordgo.WithContext(ctx)))
}

func (g *Gateway) interaction(in *core.Interaction) (*discordgo.Interaction, error) {
	if in == nil || in.ID == "" || in.Token == "" {
		return nil, errors.New("discord: interaction has no id or token")
	}
	g.mu.Lock()
	appID := g.appID
	g.mu.Unlock()
	return fromInteraction(in, appID), nil
}

func (g *Gateway) Respond(ctx context.Context, in *core.Interaction, m core.Message, ephemeral bool) error {
	di, err := g.interaction(in)
	if err != nil {
		return err
	}
	return mapError("respond", g.s.InteractionRespond(di, toResponse(m, ephemeral), discordgo.WithContext(ctx)))
}

func (g *Gateway) Defer(ctx context.Context, in *core.Interaction, ephemeral bool) error {
	di, err := g.interaction(in)
	if err != nil {
		return err
	}
	return mapError("defer", g.s.InteractionRespond(di, toDeferResponse(ephemeral), discordgo.WithContext(ctx)))
}

func (g *Gateway) FollowUp(ctx context.Context, in *core.Interaction, m core.Message, ephemeral bool) error {
	di, err := g.interaction(in)
	if err != nil {
		return err
	}
	if di.AppID == "" {
		return errors.New("discord: follow-up before Open")
	}
	_, err = g.s.FollowupMessageCreate(di, true, toFollowUp(m, ephemeral), discordgo.WithContext(ctx))
	return mapError("follow up", err)
}

func (g *Gateway) Update(ctx context.Context, in *core.Interaction, m core.Message) error {
	di, err := g.interaction(in)
	if err != nil {
		return err
	}
	return mapError("update", g.s.InteractionRespond(di, toUpdateResponse(m), discordgo.WithContext(ctx)))
}
