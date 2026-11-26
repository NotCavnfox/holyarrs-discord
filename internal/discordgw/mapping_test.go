package discordgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

func sampleMessage() core.Message {
	return core.Message{
		Content: "hello",
		Embeds: []core.Embed{{
			Title:       "Dune",
			Description: "Sand",
			URL:         "https://example.test/dune",
			Color:       0x00ff00,
			Fields:      []core.Field{{Name: "Year", Value: "2021", Inline: true}},
			Thumbnail:   "attachment://poster.jpg",
			Image:       "https://example.test/i.png",
			Footer:      "foot",
			Time:        time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600)),
		}},
		Rows: [][]core.Button{
			{
				{Label: "Yes", CustomID: "vote:yes:1", Style: core.ButtonSuccess},
				{Label: "No", CustomID: "vote:no:1", Style: core.ButtonDanger, Disabled: true},
			},
			{},
			{{Label: "Open", URL: "https://example.test", CustomID: "ignored", Style: core.ButtonLink}},
		},
		Files: []core.File{{Name: "poster.jpg", ContentType: "image/jpeg", Data: []byte("jpg")}, {Name: "raw.bin", Data: []byte{1}}},
	}
}

func TestToButtonStyle(t *testing.T) {
	cases := map[core.ButtonStyle]discordgo.ButtonStyle{
		core.ButtonPrimary:    discordgo.PrimaryButton,
		core.ButtonSecondary:  discordgo.SecondaryButton,
		core.ButtonSuccess:    discordgo.SuccessButton,
		core.ButtonDanger:     discordgo.DangerButton,
		core.ButtonLink:       discordgo.LinkButton,
		core.ButtonStyle(999): discordgo.PrimaryButton,
	}
	for in, want := range cases {
		if got := toButtonStyle(in); got != want {
			t.Errorf("style %d: got %d want %d", in, got, want)
		}
	}
}

func TestToComponents(t *testing.T) {
	got := toComponents(sampleMessage().Rows)
	if len(got) != 2 {
		t.Fatalf("rows: got %d want 2 (empty row dropped)", len(got))
	}
	row0 := got[0].(discordgo.ActionsRow)
	if len(row0.Components) != 2 {
		t.Fatalf("row 0 buttons: %d", len(row0.Components))
	}
	yes := row0.Components[0].(discordgo.Button)
	if yes.Label != "Yes" || yes.CustomID != "vote:yes:1" || yes.Style != discordgo.SuccessButton || yes.URL != "" || yes.Disabled {
		t.Errorf("yes button: %+v", yes)
	}
	no := row0.Components[1].(discordgo.Button)
	if !no.Disabled || no.Style != discordgo.DangerButton {
		t.Errorf("no button: %+v", no)
	}
	link := got[1].(discordgo.ActionsRow).Components[0].(discordgo.Button)
	if link.Style != discordgo.LinkButton || link.URL != "https://example.test" || link.CustomID != "" {
		t.Errorf("link button: %+v", link)
	}

	// A non-link button never carries a URL.
	b := toButton(core.Button{Label: "x", CustomID: "a:b", URL: "https://nope.test"})
	if b.URL != "" || b.CustomID != "a:b" {
		t.Errorf("plain button: %+v", b)
	}

	empty := toComponents(nil)
	if empty == nil || len(empty) != 0 {
		t.Errorf("nil rows must map to an empty, non-nil slice: %#v", empty)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"type":1`) || !strings.Contains(string(raw), `"custom_id":"vote:yes:1"`) {
		t.Errorf("component json: %s", raw)
	}
}

func TestToEmbed(t *testing.T) {
	e := toEmbed(sampleMessage().Embeds[0])
	if e.Title != "Dune" || e.Description != "Sand" || e.URL != "https://example.test/dune" || e.Color != 0x00ff00 {
		t.Errorf("basics: %+v", e)
	}
	if len(e.Fields) != 1 || *e.Fields[0] != (discordgo.MessageEmbedField{Name: "Year", Value: "2021", Inline: true}) {
		t.Errorf("fields: %+v", e.Fields)
	}
	if e.Thumbnail == nil || e.Thumbnail.URL != "attachment://poster.jpg" {
		t.Errorf("thumbnail: %+v", e.Thumbnail)
	}
	if e.Image == nil || e.Image.URL != "https://example.test/i.png" {
		t.Errorf("image: %+v", e.Image)
	}
	if e.Footer == nil || e.Footer.Text != "foot" {
		t.Errorf("footer: %+v", e.Footer)
	}
	if e.Timestamp != "2026-01-02T02:04:05Z" {
		t.Errorf("timestamp: %q", e.Timestamp)
	}

	bare := toEmbed(core.Embed{Title: "t"})
	if bare.Thumbnail != nil || bare.Image != nil || bare.Footer != nil || bare.Timestamp != "" || bare.Fields != nil {
		t.Errorf("bare embed has extras: %+v", bare)
	}
	if got := toEmbeds(nil); got == nil || len(got) != 0 {
		t.Errorf("nil embeds must map to an empty, non-nil slice")
	}
}

func TestToFiles(t *testing.T) {
	if toFiles(nil) != nil {
		t.Error("no files must map to nil")
	}
	fs := toFiles(sampleMessage().Files)
	if len(fs) != 2 {
		t.Fatalf("files: %d", len(fs))
	}
	data, _ := io.ReadAll(fs[0].Reader)
	if fs[0].Name != "poster.jpg" || fs[0].ContentType != "image/jpeg" || string(data) != "jpg" {
		t.Errorf("file 0: %+v %q", fs[0], data)
	}
	if fs[1].ContentType != "application/octet-stream" {
		t.Errorf("default content type: %q", fs[1].ContentType)
	}
}

func checkMentions(t *testing.T, am *discordgo.MessageAllowedMentions) {
	t.Helper()
	if am == nil || len(am.Parse) != 1 || am.Parse[0] != discordgo.AllowedMentionTypeUsers {
		t.Errorf("allowed mentions must be users only: %+v", am)
	}
}

func TestToSend(t *testing.T) {
	s := toSend(sampleMessage())
	if s.Content != "hello" || len(s.Embeds) != 1 || len(s.Components) != 2 || len(s.Files) != 2 || s.Flags != 0 {
		t.Errorf("send: %+v", s)
	}
	checkMentions(t, s.AllowedMentions)
}

func TestToEdit(t *testing.T) {
	e := toEdit("chan", "msg", sampleMessage())
	if e.Channel != "chan" || e.ID != "msg" {
		t.Errorf("ids: %q %q", e.Channel, e.ID)
	}
	if e.Content == nil || *e.Content != "hello" || e.Embeds == nil || len(*e.Embeds) != 1 ||
		e.Components == nil || len(*e.Components) != 2 || len(e.Files) != 2 {
		t.Errorf("edit: %+v", e)
	}
	if e.Attachments == nil || len(*e.Attachments) != 0 {
		t.Errorf("edit must drop old attachments")
	}
	checkMentions(t, e.AllowedMentions)

	// An empty message clears content, embeds, components and attachments.
	raw, err := json.Marshal(toEdit("c", "m", core.Message{}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"content": `""`, "embeds": `[]`, "components": `[]`, "attachments": `[]`}
	for k, v := range want {
		if string(got[k]) != v {
			t.Errorf("edit json %s: got %s want %s (full: %s)", k, got[k], v, raw)
		}
	}
}

func TestToResponse(t *testing.T) {
	r := toResponse(sampleMessage(), true)
	if r.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Errorf("type: %v", r.Type)
	}
	if r.Data.Flags != discordgo.MessageFlagsEphemeral || r.Data.Content != "hello" ||
		len(r.Data.Embeds) != 1 || len(r.Data.Components) != 2 || len(r.Data.Files) != 2 || r.Data.Attachments != nil {
		t.Errorf("data: %+v", r.Data)
	}
	checkMentions(t, r.Data.AllowedMentions)
	if toResponse(core.Message{}, false).Data.Flags != 0 {
		t.Error("public response must have no flags")
	}
}

func TestToDeferResponse(t *testing.T) {
	r := toDeferResponse(true)
	if r.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource || r.Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Errorf("defer: %+v %+v", r, r.Data)
	}
	if toDeferResponse(false).Data.Flags != 0 {
		t.Error("public defer must have no flags")
	}
}

func TestToUpdateResponse(t *testing.T) {
	r := toUpdateResponse(core.Message{Content: "done"})
	if r.Type != discordgo.InteractionResponseUpdateMessage {
		t.Errorf("type: %v", r.Type)
	}
	if r.Data.Attachments == nil || len(*r.Data.Attachments) != 0 {
		t.Error("update must drop old attachments")
	}
	raw, err := json.Marshal(r.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"components":[]`, `"embeds":[]`, `"attachments":[]`, `"content":"done"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("update json lacks %s: %s", want, raw)
		}
	}
}

func TestToFollowUp(t *testing.T) {
	f := toFollowUp(sampleMessage(), true)
	if f.Flags != discordgo.MessageFlagsEphemeral || f.Content != "hello" || len(f.Embeds) != 1 ||
		len(f.Components) != 2 || len(f.Files) != 2 {
		t.Errorf("follow-up: %+v", f)
	}
	checkMentions(t, f.AllowedMentions)
	if toFollowUp(core.Message{}, false).Flags != 0 {
		t.Error("public follow-up must have no flags")
	}
}

func noop(context.Context, *core.Interaction) error { return nil }

func TestToCommandPlain(t *testing.T) {
	c, err := toCommand(core.Command{
		Name:        "request",
		Description: "Ask for a title",
		Options: []core.Option{
			{Name: "year", Description: "Year", Type: core.OptInt, Choices: []string{"2024", "2025"}},
			{Name: "title", Description: "Name", Type: core.OptString, Required: true},
			{Name: "kind", Description: "Kind", Type: core.OptString, Choices: []string{"movie", "show"}},
			{Name: "quiet", Description: "Quiet", Type: core.OptBool},
			{Name: "who", Description: "Who", Type: core.OptUser},
		},
		Handle: noop,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "request" || c.Description != "Ask for a title" || c.Type != discordgo.ChatApplicationCommand {
		t.Errorf("command: %+v", c)
	}
	if c.DefaultMemberPermissions != nil {
		t.Error("public command must not set default permissions")
	}
	var names []string
	for _, o := range c.Options {
		names = append(names, o.Name)
	}
	if got := strings.Join(names, ","); got != "title,year,kind,quiet,who" {
		t.Errorf("required options must come first: %s", got)
	}
	title, year, kind, quiet, who := c.Options[0], c.Options[1], c.Options[2], c.Options[3], c.Options[4]
	if !title.Required || title.Type != discordgo.ApplicationCommandOptionString || title.Description != "Name" {
		t.Errorf("title: %+v", title)
	}
	if year.Type != discordgo.ApplicationCommandOptionInteger || len(year.Choices) != 2 ||
		year.Choices[0].Name != "2024" || year.Choices[0].Value != int64(2024) {
		t.Errorf("year: %+v", year)
	}
	if len(kind.Choices) != 2 || kind.Choices[1].Name != "show" || kind.Choices[1].Value != "show" {
		t.Errorf("kind: %+v", kind)
	}
	if quiet.Type != discordgo.ApplicationCommandOptionBoolean || who.Type != discordgo.ApplicationCommandOptionUser {
		t.Errorf("types: %v %v", quiet.Type, who.Type)
	}
}

func TestToCommandAdminAndSub(t *testing.T) {
	admin, err := toCommand(core.Command{Name: "purge", Description: "d", AdminOnly: true, Handle: noop}, false)
	if err != nil {
		t.Fatal(err)
	}
	if admin.DefaultMemberPermissions == nil || *admin.DefaultMemberPermissions != discordgo.PermissionManageGuild {
		t.Errorf("admin command must default to Manage Server: %v", admin.DefaultMemberPermissions)
	}
	raw, _ := json.Marshal(admin)
	if !strings.Contains(string(raw), `"default_member_permissions":"32"`) {
		t.Errorf("admin json: %s", raw)
	}

	parent := core.Command{
		Name:        "purge-vote",
		Description: "Votes",
		Options:     []core.Option{{Name: "unused", Type: core.OptString}},
		Sub: []core.Command{
			{Name: "start", Description: "Start", AdminOnly: true, Handle: noop,
				Options: []core.Option{{Name: "days", Description: "Days", Type: core.OptInt, Required: true}}},
			{Name: "status", Description: "Status", Handle: noop},
		},
	}
	c, err := toCommand(parent, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultMemberPermissions != nil {
		t.Error("mixed parent must stay visible")
	}
	if len(c.Options) != 2 {
		t.Fatalf("parent options are the sub-commands only: %+v", c.Options)
	}
	start := c.Options[0]
	if start.Type != discordgo.ApplicationCommandOptionSubCommand || start.Name != "start" || start.Description != "Start" ||
		len(start.Options) != 1 || start.Options[0].Name != "days" || !start.Options[0].Required {
		t.Errorf("start: %+v", start)
	}

	parent.Sub[1].AdminOnly = true
	c, _ = toCommand(parent, false)
	if c.DefaultMemberPermissions == nil {
		t.Error("parent whose sub-commands are all admin-only must default to Manage Server")
	}
	parent.Sub[1].AdminOnly = false
	parent.AdminOnly = true
	c, _ = toCommand(parent, false)
	if c.DefaultMemberPermissions == nil {
		t.Error("admin parent must default to Manage Server")
	}
}

// With an admin role configured its holders may lack Manage Server, so no
// default permission may hide admin commands from them; the routes still
// carry adminOnly for the runtime check.
func TestAdminCommandsVisibleToAdminRole(t *testing.T) {
	cmds := []core.Command{
		{Name: "purge", Description: "d", AdminOnly: true, Handle: noop},
		{Name: "link-admin", Description: "d", AdminOnly: true, Sub: []core.Command{
			{Name: "clear", Description: "c", AdminOnly: true, Handle: noop},
			{Name: "status", Description: "s", AdminOnly: true, Handle: noop},
		}},
		{Name: "public", Description: "d", Handle: noop},
	}
	withRole, err := toCommands(cmds, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range withRole {
		if c.DefaultMemberPermissions != nil {
			t.Errorf("%s: admin role configured, want no default permission, got %d", c.Name, *c.DefaultMemberPermissions)
		}
		raw, _ := json.Marshal(c)
		if strings.Contains(string(raw), "default_member_permissions") {
			t.Errorf("%s json: %s", c.Name, raw)
		}
	}
	noRole, err := toCommands(cmds, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, true, false} {
		if got := noRole[i].DefaultMemberPermissions != nil; got != want {
			t.Errorf("%s: no admin role, default permission set = %v, want %v", noRole[i].Name, got, want)
		}
	}
	routes, err := buildRoutes(cmds)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"purge", "link-admin clear", "link-admin status"} {
		if !routes[k].adminOnly {
			t.Errorf("route %q must stay admin-only at runtime", k)
		}
	}
	if routes["public"].adminOnly {
		t.Error("public route became admin-only")
	}
}

// Transport errors carry the request URL, which for interaction and webhook
// calls holds the interaction token. None of it may reach the error text.
func TestMapErrorNeverShowsInteractionTokens(t *testing.T) {
	const token = "aW50ZXJhY3Rpb246c2VjcmV0LXRva2Vu"
	inner := errors.New("dial tcp: connection refused")
	cases := []struct {
		name string
		err  error
		is   error
	}{
		{"url.Error on a callback", &url.Error{Op: "Post",
			URL: "https://discord.com/api/v9/interactions/123456/" + token + "/callback", Err: inner}, inner},
		{"url.Error on a webhook", &url.Error{Op: "Patch",
			URL: "https://discord.com/api/v9/webhooks/9988/" + token + "/messages/@original", Err: inner}, inner},
		{"wrapped url.Error", fmt.Errorf("retry 2: %w", &url.Error{Op: "Post",
			URL: "https://discord.com/api/v9/webhooks/9988/" + token, Err: inner}), inner},
		{"url.Error around a deadline", &url.Error{Op: "Post",
			URL: "https://discord.com/api/v9/interactions/123456/" + token + "/callback", Err: context.DeadlineExceeded},
			context.DeadlineExceeded},
		{"url.Error whose cause repeats the path", &url.Error{Op: "Post", URL: "https://discord.com/x",
			Err: errors.New("stopped after redirect to /webhooks/9988/" + token + "/messages/1")}, nil},
		{"plain text with a callback path", errors.New(`Post "https://discord.com/api/v9/interactions/123456/` + token + `/callback": EOF`), nil},
		{"plain text with a webhook path", errors.New("PATCH /webhooks/9988/" + token + "?wait=true failed"), nil},
		{"REST message with a path", restErr(500, 0, "cannot route /webhooks/9988/"+token), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapError("respond", c.err)
			text := got.Error()
			if strings.Contains(text, token) {
				t.Fatalf("token leaked: %s", text)
			}
			if strings.Contains(fmt.Sprintf("%v %+v %s", got, got, got), token) {
				t.Fatal("token leaked through formatting")
			}
			if !strings.HasPrefix(text, "discord respond: ") {
				t.Errorf("text = %q", text)
			}
			if c.is != nil && !errors.Is(got, c.is) {
				t.Errorf("chain lost %v: %s", c.is, text)
			}
			var ue *url.Error
			if errors.As(got, &ue) {
				t.Errorf("url.Error still in the chain: %s", text)
			}
		})
	}
	// Scrubbing leaves harmless text alone and keeps the cause readable.
	got := mapError("send", &url.Error{Op: "Post", URL: "https://discord.com/api/v9/channels/1/messages", Err: errors.New("EOF")})
	if got.Error() != "discord send: EOF" {
		t.Errorf("got %q", got)
	}
	if s := redactPaths("see /interactions/1/tok/callback and /webhooks/2/tok2"); s != "see /interactions/[redacted]/[redacted]/callback and /webhooks/[redacted]/[redacted]" {
		t.Errorf("redactPaths = %q", s)
	}
}

func TestToCommandErrors(t *testing.T) {
	bad := []core.Command{
		{Name: "a", Options: []core.Option{{Name: "o", Type: core.OptionType(42)}}},
		{Name: "b", Options: []core.Option{{Name: "o", Type: core.OptInt, Choices: []string{"x"}}}},
		{Name: "c", Options: []core.Option{{Name: "o", Type: core.OptBool, Choices: []string{"true"}}}},
		{Name: "d", Sub: []core.Command{{Name: "s", Sub: []core.Command{{Name: "deep"}}}}},
		{Name: "e", Sub: []core.Command{{Name: "s", Options: []core.Option{{Name: "o", Type: core.OptionType(42)}}}}},
	}
	for _, c := range bad {
		if _, err := toCommand(c, false); err == nil {
			t.Errorf("command %q: want an error", c.Name)
		}
		if _, err := toCommands([]core.Command{{Name: "ok", Handle: noop}, c}, false); err == nil {
			t.Errorf("commands with %q: want an error", c.Name)
		}
	}
	got, err := toCommands([]core.Command{{Name: "x", Handle: noop}, {Name: "y", Handle: noop}}, false)
	if err != nil || len(got) != 2 || got[1].Name != "y" {
		t.Errorf("toCommands: %v %v", got, err)
	}
	if got, err := toCommands(nil, false); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no commands must map to an empty, non-nil list: %v %v", got, err)
	}
}

func TestBuildRoutes(t *testing.T) {
	routes, err := buildRoutes([]core.Command{
		{Name: "me", Handle: noop},
		{Name: "purge", AdminOnly: true, Handle: noop},
		{Name: "vote", Sub: []core.Command{
			{Name: "start", AdminOnly: true, Handle: noop},
			{Name: "status", Handle: noop},
		}},
		{Name: "mod", AdminOnly: true, Sub: []core.Command{{Name: "log", Handle: noop}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"me": false, "purge": true, "vote start": true, "vote status": false, "mod log": true}
	if len(routes) != len(want) {
		t.Errorf("routes: %v", routes)
	}
	for k, admin := range want {
		r, ok := routes[k]
		if !ok || r.handle == nil || r.adminOnly != admin {
			t.Errorf("route %q: ok=%v admin=%v", k, ok, r.adminOnly)
		}
	}
	if _, ok := routes["vote"]; ok {
		t.Error("a parent is not callable by itself")
	}

	bad := [][]core.Command{
		{{Name: "a"}},
		{{Name: "a", Handle: noop}, {Name: "a", Handle: noop}},
		{{Name: "a", Sub: []core.Command{{Name: "s"}}}},
		{{Name: "a", Sub: []core.Command{{Name: "s", Handle: noop}, {Name: "s", Handle: noop}}}},
	}
	for i, cmds := range bad {
		if _, err := buildRoutes(cmds); err == nil {
			t.Errorf("bad set %d: want an error", i)
		}
	}
}

func TestOptionText(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{nil, ""},
		{"text", "text"},
		{true, "true"},
		{false, "false"},
		{float64(42), "42"},
		{float64(-3), "-3"},
		{1.5, "1.5"},
		{7, "7"},
		{int64(8), "8"},
		{uint8(9), "9"},
	}
	for _, c := range cases {
		got := optionText(&discordgo.ApplicationCommandInteractionDataOption{Value: c.v})
		if got != c.want {
			t.Errorf("%#v: got %q want %q", c.v, got, c.want)
		}
	}
}

// decode builds an interaction the way the gateway delivers it.
func decode(t *testing.T, raw string) *discordgo.Interaction {
	t.Helper()
	var i discordgo.Interaction
	if err := json.Unmarshal([]byte(raw), &i); err != nil {
		t.Fatalf("decode interaction: %v", err)
	}
	return &i
}

const slashJSON = `{
  "id": "111", "application_id": "999", "type": 2, "token": "tok",
  "guild_id": "g1", "channel_id": "c1",
  "member": {"nick": "Nick", "roles": ["r1", "r2"], "permissions": "1024",
             "user": {"id": "u1", "username": "plain", "global_name": "Global"}},
  "data": {"id": "5", "name": "request", "type": 1, "options": [
    {"name": "title", "type": 3, "value": "Dune"},
    {"name": "year", "type": 4, "value": 2021},
    {"name": "quiet", "type": 5, "value": true},
    {"name": "who", "type": 6, "value": "424242424242424242"}
  ]}
}`

const subJSON = `{
  "id": "112", "type": 2, "token": "tok2", "guild_id": "g1", "channel_id": "c1",
  "member": {"roles": [], "permissions": "32", "user": {"id": "u2", "username": "plain"}},
  "data": {"id": "6", "name": "purge-vote", "type": 1, "options": [
    {"name": "start", "type": 1, "options": [{"name": "days", "type": 4, "value": 30}]}
  ]}
}`

const buttonJSON = `{
  "id": "113", "type": 3, "token": "tok3", "guild_id": "g1", "channel_id": "c1",
  "message": {"id": "m9", "channel_id": "c1"},
  "member": {"roles": ["admins"], "permissions": "0", "user": {"id": "u3", "username": "plain", "global_name": "Global"}},
  "data": {"custom_id": "vote:yes:7", "component_type": 2}
}`

func TestToInteractionSlash(t *testing.T) {
	in, ok := toInteraction(decode(t, slashJSON), "admins")
	if !ok {
		t.Fatal("slash command must map")
	}
	if in.ID != "111" || in.Token != "tok" || in.GuildID != "g1" || in.ChannelID != "c1" ||
		in.UserID != "u1" || in.UserName != "Nick" || in.IsAdmin || in.Command != "request" ||
		in.CustomID != "" || in.MessageID != "" {
		t.Errorf("interaction: %+v", in)
	}
	want := map[string]string{"title": "Dune", "year": "2021", "quiet": "true", "who": "424242424242424242"}
	if len(in.Options) != len(want) {
		t.Errorf("options: %v", in.Options)
	}
	for k, v := range want {
		if in.Options[k] != v {
			t.Errorf("option %s: got %q want %q", k, in.Options[k], v)
		}
	}
}

func TestToInteractionSub(t *testing.T) {
	in, ok := toInteraction(decode(t, subJSON), "admins")
	if !ok {
		t.Fatal("sub-command must map")
	}
	if in.Command != "purge-vote start" || in.Options["days"] != "30" || len(in.Options) != 1 {
		t.Errorf("sub-command: %q %v", in.Command, in.Options)
	}
	if !in.IsAdmin {
		t.Error("Manage Server must count as admin")
	}
	if in.UserName != "plain" {
		t.Errorf("user name: %q", in.UserName)
	}
}

func TestToInteractionButton(t *testing.T) {
	in, ok := toInteraction(decode(t, buttonJSON), "admins")
	if !ok {
		t.Fatal("button press must map")
	}
	if in.CustomID != "vote:yes:7" || in.MessageID != "m9" || in.Command != "" || in.UserID != "u3" || in.UserName != "Global" {
		t.Errorf("button: %+v", in)
	}
	if !in.IsAdmin {
		t.Error("the admin role must count as admin")
	}
	if in.Options == nil {
		t.Error("options must never be nil")
	}
}

func TestToInteractionOther(t *testing.T) {
	if _, ok := toInteraction(nil, ""); ok {
		t.Error("nil must not map")
	}
	for _, typ := range []discordgo.InteractionType{
		discordgo.InteractionPing, discordgo.InteractionApplicationCommandAutocomplete, discordgo.InteractionModalSubmit,
	} {
		if _, ok := toInteraction(&discordgo.Interaction{Type: typ}, ""); ok {
			t.Errorf("type %v must not map", typ)
		}
	}
	// Mismatched data must not panic.
	if _, ok := toInteraction(&discordgo.Interaction{Type: discordgo.InteractionApplicationCommand}, ""); ok {
		t.Error("command with no data must not map")
	}
	if _, ok := toInteraction(&discordgo.Interaction{Type: discordgo.InteractionMessageComponent}, ""); ok {
		t.Error("component with no data must not map")
	}
	// A DM call has User and no Member.
	in, ok := toInteraction(&discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
		User: &discordgo.User{ID: "u9", Username: "dm"},
		Data: discordgo.ApplicationCommandInteractionData{Name: "me"},
	}, "admins")
	if !ok || in.UserID != "u9" || in.UserName != "dm" || in.IsAdmin {
		t.Errorf("dm call: %+v %v", in, ok)
	}
}

func TestIsAdmin(t *testing.T) {
	cases := []struct {
		name string
		m    *discordgo.Member
		role string
		want bool
	}{
		{"nil member", nil, "r", false},
		{"nothing", &discordgo.Member{Roles: []string{"x"}}, "r", false},
		{"role", &discordgo.Member{Roles: []string{"x", "r"}}, "r", true},
		{"no role set", &discordgo.Member{Roles: []string{""}}, "", false},
		{"manage server", &discordgo.Member{Permissions: discordgo.PermissionManageGuild}, "", true},
		{"administrator", &discordgo.Member{Permissions: discordgo.PermissionAdministrator | 1024}, "r", true},
		{"other perms", &discordgo.Member{Permissions: discordgo.PermissionManageMessages}, "r", false},
	}
	for _, c := range cases {
		if got := isAdmin(c.m, c.role); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestFromInteraction(t *testing.T) {
	di := fromInteraction(&core.Interaction{ID: "1", Token: "t"}, "app")
	if di.ID != "1" || di.Token != "t" || di.AppID != "app" {
		t.Errorf("from: %+v", di)
	}
}

func restErr(status, code int, msg string) *discordgo.RESTError {
	req, _ := http.NewRequest(http.MethodPost, "https://discord.test/api", nil)
	req.Header.Set("Authorization", "Bot super-secret-token")
	e := &discordgo.RESTError{Request: req, Response: &http.Response{StatusCode: status}}
	if code != 0 || msg != "" {
		e.Message = &discordgo.APIErrorMessage{Code: code, Message: msg}
	}
	return e
}

func TestMapError(t *testing.T) {
	if mapError("x", nil) != nil {
		t.Error("nil must stay nil")
	}
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"403", restErr(403, 50013, "Missing Permissions"), core.ErrForbidden},
		{"dm closed", restErr(403, 50007, "Cannot send messages to this user"), core.ErrForbidden},
		{"dm closed odd status", restErr(400, 50007, ""), core.ErrForbidden},
		{"404", restErr(404, 10008, "Unknown Message"), core.ErrNotFound},
		{"404 no body", restErr(404, 0, ""), core.ErrNotFound},
		{"429 rest", restErr(429, 0, ""), core.ErrRateLimited},
		{"429 pointer", &discordgo.RateLimitError{RateLimit: &discordgo.RateLimit{TooManyRequests: &discordgo.TooManyRequests{}, URL: "u"}}, core.ErrRateLimited},
		{"429 value", discordgo.RateLimitError{RateLimit: &discordgo.RateLimit{TooManyRequests: &discordgo.TooManyRequests{}, URL: "u"}}, core.ErrRateLimited},
		{"wrapped 404", errors.Join(errors.New("outer"), restErr(404, 0, "")), core.ErrNotFound},
		{"cancelled", context.Canceled, context.Canceled},
		{"deadline", fmt.Errorf("get: %w", context.DeadlineExceeded), context.DeadlineExceeded},
	}
	for _, c := range cases {
		got := mapError("send", c.err)
		if !errors.Is(got, c.want) {
			t.Errorf("%s: %v is not %v", c.name, got, c.want)
		}
		if !strings.HasPrefix(got.Error(), "discord send: ") {
			t.Errorf("%s: no op prefix: %v", c.name, got)
		}
		if strings.Contains(got.Error(), "super-secret-token") {
			t.Errorf("%s: token leaked: %v", c.name, got)
		}
	}

	other := mapError("send", restErr(500, 0, "boom"))
	for _, e := range []error{core.ErrForbidden, core.ErrNotFound, core.ErrRateLimited} {
		if errors.Is(other, e) {
			t.Errorf("500 must not map to %v", e)
		}
	}
	if !strings.Contains(other.Error(), "http 500") || !strings.Contains(other.Error(), "boom") {
		t.Errorf("500 text: %v", other)
	}

	plain := errors.New("dial failed")
	if got := mapError("send", plain); !errors.Is(got, plain) {
		t.Errorf("plain errors must stay in the chain: %v", got)
	}
	if got := mapError("dm", restErr(403, 50007, "Cannot send messages to this user")); !strings.Contains(got.Error(), "code 50007") {
		t.Errorf("detail missing: %v", got)
	}
}

func TestScrub(t *testing.T) {
	if scrub(nil, "s") != nil {
		t.Error("nil must stay nil")
	}
	inner := errors.New("dial with abc123 failed")
	if scrub(inner, "") != inner || scrub(inner, "zzz") != inner {
		t.Error("an error without the secret must pass through unchanged")
	}
	got := scrub(fmt.Errorf("open: %w", inner), "abc123")
	if strings.Contains(got.Error(), "abc123") || !strings.Contains(got.Error(), "[redacted]") {
		t.Errorf("secret not hidden: %v", got)
	}
	if !errors.Is(got, inner) {
		t.Error("scrub must keep the chain")
	}
}
