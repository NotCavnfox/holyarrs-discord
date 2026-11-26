package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/NotCavnfox/holyarrs-discord/internal/core"
)

// Router collects the features' slash commands and routes button presses to
// the feature that owns them.
type Router struct {
	log        *slog.Logger
	cmds       []core.Command
	components map[string]core.ComponentFeature
	err        error
}

// NewRouter indexes the features. Problems (duplicate names) are reported by
// Commands, so a router can always be built.
func NewRouter(features []core.Feature, log *slog.Logger) *Router {
	r := &Router{log: orDiscard(log), components: make(map[string]core.ComponentFeature)}
	var problems []string
	featureSeen := make(map[string]bool)
	cmdOwner := make(map[string]string)
	for i, f := range features {
		if f == nil {
			problems = append(problems, fmt.Sprintf("feature %d is nil", i))
			continue
		}
		name := f.Name()
		switch {
		case name == "" || strings.Contains(name, ":"):
			problems = append(problems, fmt.Sprintf("invalid feature name %q", name))
			continue
		case featureSeen[name]:
			problems = append(problems, fmt.Sprintf("duplicate feature name %q", name))
			continue
		}
		featureSeen[name] = true
		if cf, ok := f.(core.ComponentFeature); ok {
			r.components[name] = cf
		}
		for _, c := range f.Commands() {
			if owner, dup := cmdOwner[c.Name]; dup {
				problems = append(problems, fmt.Sprintf("duplicate command %q (features %q and %q)", c.Name, owner, name))
				continue
			}
			cmdOwner[c.Name] = name
			r.cmds = append(r.cmds, c)
		}
	}
	if len(problems) > 0 {
		r.err = errors.New("app: " + strings.Join(problems, "; "))
	}
	return r
}

// Commands returns every feature's commands in feature order. It fails when
// two commands or two features share a name.
func (r *Router) Commands() ([]core.Command, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]core.Command(nil), r.cmds...), nil
}

// Handle is a core.InteractionHandler: it sends a component interaction to
// the ComponentFeature whose Name is the CustomID prefix before the first ":".
func (r *Router) Handle(ctx context.Context, in *core.Interaction) error {
	if in == nil {
		return errors.New("app: nil interaction")
	}
	if in.CustomID == "" {
		return errors.New("app: interaction has no custom id")
	}
	prefix, _, _ := strings.Cut(in.CustomID, ":")
	f, ok := r.components[prefix]
	if !ok {
		r.log.Warn("button press for an unknown feature", "prefix", prefix)
		return fmt.Errorf("app: no feature handles components with prefix %q", prefix)
	}
	return f.HandleComponent(ctx, in)
}
