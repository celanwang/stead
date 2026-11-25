// Package intent translates a user's words into a draft for the owner to review.
// Drafts carry no owner identity, approval, credentials, or executable operations.
package intent

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

type Interpreter interface {
	Interpret(context.Context, string) (Draft, error)
}

type Kind string

const (
	Scheduling Kind = "scheduling"
	Briefing   Kind = "briefing"
	Tracking   Kind = "tracking"
)

type Draft struct {
	Kind       Kind      `json:"kind"`
	Goal       string    `json:"goal"`
	Scheduling *Schedule `json:"scheduling,omitempty"`
	Briefing   *Brief    `json:"briefing,omitempty"`
	Tracking   *Track    `json:"tracking,omitempty"`
}

type Schedule struct {
	Target  string `json:"target"`
	Content string `json:"content"`
}

type Brief struct {
	Sources []string `json:"sources"`
	Format  string   `json:"format"`
}

type Track struct {
	Source   string `json:"source"`
	ObjectID string `json:"object_id"`
	Watch    string `json:"watch"`
}

func present(s string, maxBytes int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && len(s) <= maxBytes
}

// Validate also checks drafts supplied by local or test interpreters.
func (d Draft) Validate() error {
	if !present(d.Goal, 4096) {
		return errors.New("draft requires a goal of at most 4096 bytes")
	}
	switch d.Kind {
	case Scheduling:
		s := d.Scheduling
		if s == nil || d.Briefing != nil || d.Tracking != nil || !present(s.Target, 512) || !present(s.Content, 8192) {
			return errors.New("scheduling draft requires only a target and content")
		}
	case Briefing:
		b := d.Briefing
		if b == nil || d.Tracking != nil || d.Scheduling != nil || !present(b.Format, 512) || len(b.Sources) == 0 || len(b.Sources) > 32 {
			return errors.New("briefing draft requires only a format and 1 to 32 sources")
		}
		for _, source := range b.Sources {
			if !present(source, 2048) {
				return errors.New("briefing draft contains a missing or oversized source")
			}
		}
	case Tracking:
		t := d.Tracking
		if t == nil || d.Scheduling != nil || d.Briefing != nil || !present(t.Source, 2048) || !present(t.ObjectID, 512) || !present(t.Watch, 4096) {
			return errors.New("tracking draft requires only a source, object ID, and change to watch")
		}
	default:
		return errors.New("unrecognized draft kind")
	}
	return nil
}
