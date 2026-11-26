package discordgw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// This file holds the pure translations between core types and discordgo
// types. Nothing here touches the network.

// adminPermissions are the permission bits that make a member an admin.
const adminPermissions int64 = discordgo.PermissionManageGuild | discordgo.PermissionAdministrator

// allowedMentions lets messages ping the people they name but never
// @everyone, @here or a role: titles and names are outside text.
func allowedMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{
		Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers},
	}
}

func toButtonStyle(s core.ButtonStyle) discordgo.ButtonStyle {
	switch s {
	case core.ButtonSecondary:
		return discordgo.SecondaryButton
	case core.ButtonSuccess:
		return discordgo.SuccessButton
	case core.ButtonDanger:
		return discordgo.DangerButton
	case core.ButtonLink:
		return discordgo.LinkButton
	default:
		return discordgo.PrimaryButton
	}
}

func toButton(b core.Button) discordgo.Button {
	out := discordgo.Button{
		Label:    b.Label,
		Style:    toButtonStyle(b.Style),
		Disabled: b.Disabled,
	}
	// A link button carries a URL and no custom id; every other style is the
	// other way round. Discord rejects a button that has both.
	if b.Style == core.ButtonLink {
		out.URL = b.URL
	} else {
		out.CustomID = b.CustomID
	}
	return out
}

// toComponents maps button rows to action rows. Empty rows are dropped. The
// result is never nil, so it encodes as [] and clears components on an edit.
func toComponents(rows [][]core.Button) []discordgo.MessageComponent {
	out := make([]discordgo.MessageComponent, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		ar := discordgo.ActionsRow{Components: make([]discordgo.MessageComponent, 0, len(row))}
		for _, b := range row {
			ar.Components = append(ar.Components, toButton(b))
		}
		out = append(out, ar)
	}
	return out
}

func toEmbed(e core.Embed) *discordgo.MessageEmbed {
	out := &discordgo.MessageEmbed{
		Type:        discordgo.EmbedTypeRich,
		Title:       e.Title,
		Description: e.Description,
		URL:         e.URL,
		Color:       e.Color,
	}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, &discordgo.MessageEmbedField{Name: f.Name, Value: f.Value, Inline: f.Inline})
	}
	if e.Thumbnail != "" {
		out.Thumbnail = &discordgo.MessageEmbedThumbnail{URL: e.Thumbnail}
	}
	if e.Image != "" {
		out.Image = &discordgo.MessageEmbedImage{URL: e.Image}
	}
	if e.Footer != "" {
		out.Footer = &discordgo.MessageEmbedFooter{Text: e.Footer}
	}
	if !e.Time.IsZero() {
		out.Timestamp = e.Time.UTC().Format(time.RFC3339)
	}
	return out
}

// toEmbeds never returns nil, so an edit with no embeds clears them.
func toEmbeds(es []core.Embed) []*discordgo.MessageEmbed {
	out := make([]*discordgo.MessageEmbed, 0, len(es))
	for _, e := range es {
		out = append(out, toEmbed(e))
	}
	return out
}

func toFiles(fs []core.File) []*discordgo.File {
	if len(fs) == 0 {
		return nil
	}
	out := make([]*discordgo.File, 0, len(fs))
	for _, f := range fs {
		ct := f.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		out = append(out, &discordgo.File{Name: f.Name, ContentType: ct, Reader: bytes.NewReader(f.Data)})
	}
	return out
}

func flags(ephemeral bool) discordgo.MessageFlags {
	if ephemeral {
		return discordgo.MessageFlagsEphemeral
	}
	return 0
}

// toSend maps a message for a channel or DM post.
func toSend(m core.Message) *discordgo.MessageSend {
	return &discordgo.MessageSend{
		Content:         m.Content,
		Embeds:          toEmbeds(m.Embeds),
		Components:      toComponents(m.Rows),
		Files:           toFiles(m.Files),
		AllowedMentions: allowedMentions(),
	}
}

// toEdit maps a message to a full replacement of an existing one: content,
// embeds and components are always sent (empty clears them), and the old
// attachments are dropped so only m.Files remain.
func toEdit(channelID, messageID string, m core.Message) *discordgo.MessageEdit {
	content := m.Content
	embeds := toEmbeds(m.Embeds)
	components := toComponents(m.Rows)
	keep := []*discordgo.MessageAttachment{}
	return &discordgo.MessageEdit{
		ID:              messageID,
		Channel:         channelID,
		Content:         &content,
		Embeds:          &embeds,
		Components:      &components,
		Attachments:     &keep,
		Files:           toFiles(m.Files),
		AllowedMentions: allowedMentions(),
	}
}

// toResponse maps a message to a first answer to an interaction.
func toResponse(m core.Message, ephemeral bool) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:         m.Content,
			Embeds:          toEmbeds(m.Embeds),
			Components:      toComponents(m.Rows),
			Files:           toFiles(m.Files),
			AllowedMentions: allowedMentions(),
			Flags:           flags(ephemeral),
		},
	}
}

func toDeferResponse(ephemeral bool) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: flags(ephemeral)},
	}
}

// toUpdateResponse maps a message to an in-place edit of the message a
// button sits on. Like toEdit it replaces everything.
func toUpdateResponse(m core.Message) *discordgo.InteractionResponse {
	keep := []*discordgo.MessageAttachment{}
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:         m.Content,
			Embeds:          toEmbeds(m.Embeds),
			Components:      toComponents(m.Rows),
			Files:           toFiles(m.Files),
			Attachments:     &keep,
			AllowedMentions: allowedMentions(),
		},
	}
}

// toFollowUp maps a message to a follow-up after Defer.
func toFollowUp(m core.Message, ephemeral bool) *discordgo.WebhookParams {
	return &discordgo.WebhookParams{
		Content:         m.Content,
		Embeds:          toEmbeds(m.Embeds),
		Components:      toComponents(m.Rows),
		Files:           toFiles(m.Files),
		AllowedMentions: allowedMentions(),
		Flags:           flags(ephemeral),
	}
}

func toOptionType(t core.OptionType) (discordgo.ApplicationCommandOptionType, error) {
	switch t {
	case core.OptString:
		return discordgo.ApplicationCommandOptionString, nil
	case core.OptInt:
		return discordgo.ApplicationCommandOptionInteger, nil
	case core.OptBool:
		return discordgo.ApplicationCommandOptionBoolean, nil
	case core.OptUser:
		return discordgo.ApplicationCommandOptionUser, nil
	}
	return 0, fmt.Errorf("unknown option type %d", t)
}

func toOption(o core.Option) (*discordgo.ApplicationCommandOption, error) {
	t, err := toOptionType(o.Type)
	if err != nil {
		return nil, fmt.Errorf("option %q: %w", o.Name, err)
	}
	out := &discordgo.ApplicationCommandOption{
		Type:        t,
		Name:        o.Name,
		Description: o.Description,
		Required:    o.Required,
	}
	for _, c := range o.Choices {
		choice := &discordgo.ApplicationCommandOptionChoice{Name: c}
		switch o.Type {
		case core.OptString:
			choice.Value = c
		case core.OptInt:
			n, err := strconv.ParseInt(c, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("option %q: choice %q is not a whole number", o.Name, c)
			}
			choice.Value = n
		default:
			return nil, fmt.Errorf("option %q: only text and number options can have choices", o.Name)
		}
		out.Choices = append(out.Choices, choice)
	}
	return out, nil
}

// toOptions puts required options first, as Discord demands, keeping the
// given order otherwise.
func toOptions(opts []core.Option) ([]*discordgo.ApplicationCommandOption, error) {
	out := make([]*discordgo.ApplicationCommandOption, 0, len(opts))
	for _, pass := range []bool{true, false} {
		for _, o := range opts {
			if o.Required != pass {
				continue
			}
			do, err := toOption(o)
			if err != nil {
				return nil, err
			}
			out = append(out, do)
		}
	}
	return out, nil
}

// toCommand maps one top-level command. A command with Sub becomes a parent
// whose options are the sub-commands. Discord can only hide a whole command,
// so the Manage Server default applies when the parent is admin-only or
// every sub-command is; mixed parents rely on the runtime check alone.
//
// adminRole says an admin role is configured. Its holders need not have
// Manage Server, and a default permission would hide the command from them,
// so then no default is set and the runtime IsAdmin check is the only gate.
func toCommand(c core.Command, adminRole bool) (*discordgo.ApplicationCommand, error) {
	out := &discordgo.ApplicationCommand{
		Type:        discordgo.ChatApplicationCommand,
		Name:        c.Name,
		Description: c.Description,
	}
	admin := c.AdminOnly
	if len(c.Sub) == 0 {
		opts, err := toOptions(c.Options)
		if err != nil {
			return nil, fmt.Errorf("command %q: %w", c.Name, err)
		}
		out.Options = opts
	} else {
		allAdmin := true
		for _, sub := range c.Sub {
			if len(sub.Sub) > 0 {
				return nil, fmt.Errorf("command %q: sub-command %q has its own sub-commands", c.Name, sub.Name)
			}
			opts, err := toOptions(sub.Options)
			if err != nil {
				return nil, fmt.Errorf("command %q %q: %w", c.Name, sub.Name, err)
			}
			out.Options = append(out.Options, &discordgo.ApplicationCommandOption{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        sub.Name,
				Description: sub.Description,
				Options:     opts,
			})
			allAdmin = allAdmin && sub.AdminOnly
		}
		admin = admin || allAdmin
	}
	if admin && !adminRole {
		perm := int64(discordgo.PermissionManageGuild)
		out.DefaultMemberPermissions = &perm
	}
	return out, nil
}

func toCommands(cmds []core.Command, adminRole bool) ([]*discordgo.ApplicationCommand, error) {
	out := make([]*discordgo.ApplicationCommand, 0, len(cmds))
	for _, c := range cmds {
		dc, err := toCommand(c, adminRole)
		if err != nil {
			return nil, err
		}
		out = append(out, dc)
	}
	return out, nil
}

// route is one callable command: "name" or "parent sub".
type route struct {
	handle    func(ctx context.Context, in *core.Interaction) error
	adminOnly bool
}

// buildRoutes indexes the handlers by the text Interaction.Command carries.
// A sub-command is admin-only when it or its parent says so.
func buildRoutes(cmds []core.Command) (map[string]route, error) {
	routes := make(map[string]route)
	add := func(key string, h func(context.Context, *core.Interaction) error, admin bool) error {
		if h == nil {
			return fmt.Errorf("command %q has no handler", key)
		}
		if _, dup := routes[key]; dup {
			return fmt.Errorf("command %q is registered twice", key)
		}
		routes[key] = route{handle: h, adminOnly: admin}
		return nil
	}
	seen := make(map[string]bool)
	for _, c := range cmds {
		if seen[c.Name] {
			return nil, fmt.Errorf("command %q is registered twice", c.Name)
		}
		seen[c.Name] = true
		if len(c.Sub) == 0 {
			if err := add(c.Name, c.Handle, c.AdminOnly); err != nil {
				return nil, err
			}
			continue
		}
		for _, sub := range c.Sub {
			if err := add(c.Name+" "+sub.Name, sub.Handle, c.AdminOnly || sub.AdminOnly); err != nil {
				return nil, err
			}
		}
	}
	return routes, nil
}

// optionText renders one option value as text. A user option is the user id.
func optionText(o *discordgo.ApplicationCommandInteractionDataOption) string {
	switch v := o.Value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprint(v)
	}
}

// commandCall flattens slash command data into the "parent sub" name and the
// options of the innermost sub-command.
func commandCall(d discordgo.ApplicationCommandInteractionData) (string, map[string]string) {
	name := d.Name
	opts := d.Options
	for len(opts) == 1 && opts[0] != nil &&
		(opts[0].Type == discordgo.ApplicationCommandOptionSubCommand ||
			opts[0].Type == discordgo.ApplicationCommandOptionSubCommandGroup) {
		name += " " + opts[0].Name
		opts = opts[0].Options
	}
	out := make(map[string]string, len(opts))
	for _, o := range opts {
		if o != nil {
			out[o.Name] = optionText(o)
		}
	}
	return name, out
}

// isAdmin reports whether the member has the admin role, Manage Server or
// Administrator. Member.Permissions is filled by Discord on interactions.
func isAdmin(m *discordgo.Member, adminRoleID string) bool {
	if m == nil {
		return false
	}
	if m.Permissions&adminPermissions != 0 {
		return true
	}
	if adminRoleID == "" {
		return false
	}
	for _, r := range m.Roles {
		if r == adminRoleID {
			return true
		}
	}
	return false
}

// toInteraction maps a slash command call or a button press. ok is false
// for every other kind of interaction (pings, autocomplete, modals).
func toInteraction(i *discordgo.Interaction, adminRoleID string) (in *core.Interaction, ok bool) {
	if i == nil {
		return nil, false
	}
	in = &core.Interaction{
		ID:        i.ID,
		Token:     i.Token,
		GuildID:   i.GuildID,
		ChannelID: i.ChannelID,
		IsAdmin:   isAdmin(i.Member, adminRoleID),
		Options:   map[string]string{},
	}
	user := i.User
	if i.Member != nil && i.Member.User != nil {
		user = i.Member.User
	}
	if user != nil {
		in.UserID = user.ID
		in.UserName = user.Username
		if user.GlobalName != "" {
			in.UserName = user.GlobalName
		}
	}
	if i.Member != nil && i.Member.Nick != "" {
		in.UserName = i.Member.Nick
	}
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		d, isCmd := i.Data.(discordgo.ApplicationCommandInteractionData)
		if !isCmd {
			return nil, false
		}
		in.Command, in.Options = commandCall(d)
	case discordgo.InteractionMessageComponent:
		d, isComp := i.Data.(discordgo.MessageComponentInteractionData)
		if !isComp {
			return nil, false
		}
		in.CustomID = d.CustomID
		if i.Message != nil {
			in.MessageID = i.Message.ID
		}
	default:
		return nil, false
	}
	return in, true
}

// fromInteraction rebuilds the little discordgo needs to answer.
func fromInteraction(in *core.Interaction, appID string) *discordgo.Interaction {
	return &discordgo.Interaction{ID: in.ID, Token: in.Token, AppID: appID}
}

// mapError turns Discord REST failures into core errors. op names the call
// for the log. The text is built from the status and Discord's own message
// only, never from the request, so no header can leak into it. Transport
// errors (*url.Error) carry the request URL, and interaction and webhook URLs
// hold the interaction token, so those are cut down to their cause and any
// such path left in the text is redacted.
func mapError(op string, err error) error {
	if err == nil {
		return nil
	}
	err = safeError(err)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("discord %s: %w", op, err)
	}
	var rl *discordgo.RateLimitError
	var rlv discordgo.RateLimitError
	if errors.As(err, &rl) || errors.As(err, &rlv) {
		return fmt.Errorf("discord %s: %w", op, core.ErrRateLimited)
	}
	var re *discordgo.RESTError
	if !errors.As(err, &re) || re == nil {
		return fmt.Errorf("discord %s: %w", op, err)
	}
	status, code, msg := 0, 0, ""
	if re.Response != nil {
		status = re.Response.StatusCode
	}
	if re.Message != nil {
		code, msg = re.Message.Code, re.Message.Message
	}
	detail := fmt.Sprintf("http %d, code %d", status, code)
	if msg != "" {
		detail += ": " + redactPaths(msg)
	}
	var kind error
	switch {
	case code == discordgo.ErrCodeCannotSendMessagesToThisUser, status == http.StatusForbidden:
		kind = core.ErrForbidden
	case status == http.StatusNotFound:
		kind = core.ErrNotFound
	case status == http.StatusTooManyRequests:
		kind = core.ErrRateLimited
	default:
		return fmt.Errorf("discord %s: %s", op, detail)
	}
	return fmt.Errorf("discord %s: %w (%s)", op, kind, detail)
}

// tokenPath matches the REST paths that carry an interaction token:
// /interactions/<id>/<token>[/callback] and /webhooks/<app>/<token>[/...].
var tokenPath = regexp.MustCompile(`/(interactions|webhooks)/[^/\s"'?#]+/[^/\s"'?#]+`)

func redactPaths(s string) string {
	return tokenPath.ReplaceAllString(s, "/$1/[redacted]/[redacted]")
}

// safeError returns err without request URLs in its text. A *url.Error is
// replaced by its inner Err (the chain below it is kept, so context and
// rate-limit checks still work); what is left is wrapped so that Error()
// redacts token paths.
func safeError(err error) error {
	for {
		var ue *url.Error
		if !errors.As(err, &ue) || ue == nil || ue.Err == nil {
			break
		}
		err = ue.Err
	}
	if err == nil || !tokenPath.MatchString(err.Error()) {
		return err
	}
	return pathScrubbed{err}
}

type pathScrubbed struct{ err error }

func (p pathScrubbed) Error() string { return redactPaths(p.err.Error()) }
func (p pathScrubbed) Unwrap() error { return p.err }

// scrubbed hides a secret in an error's text while keeping the chain.
type scrubbed struct {
	err    error
	secret string
}

func (s scrubbed) Error() string {
	return strings.ReplaceAll(s.err.Error(), s.secret, "[redacted]")
}

func (s scrubbed) Unwrap() error { return s.err }

func scrub(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return scrubbed{err: err, secret: secret}
}
