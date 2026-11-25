package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/preferences"
	"github.com/celanwang/stead/internal/rsi"
	"github.com/celanwang/stead/internal/scenarios/tracking"
)

const trackingHelp = `Usage: stead track <command> [options]

  observe  Record a source revision and decide whether to notify.
  mode     Set an object's mode to active, muted, stopped, or taken_over.
  status   Show the latest revision and tracking mode for an object.
  history  Show recorded observations and decisions for an object.
  feed     Show unread immediate and digest entries for a project.
  ack      Acknowledge an unread feed entry by sequence number.

Run 'stead track <command> -h' for options.
`

const preferencesHelp = `Usage: stead preference <command> [options]

  set      Record a user's scoped preference.
  resolve  Show the preference in effect at a specified scope and time.
  revoke   Revoke a recorded preference.

Run 'stead preference <command> -h' for options.
`

type trackingPolicyLoader func(context.Context, *ledger.Ledger, string, string) (rsi.Policy, string, error)

func runTracking(ctx context.Context, args []string, load trackingPolicyLoader) error {
	if helpRequested(args) {
		fmt.Print(trackingHelp)
		return nil
	}
	flags := flag.NewFlagSet("track "+args[0], flag.ContinueOnError)
	var owner, project string
	flags.StringVar(&owner, "owner", "", "owner identity")
	flags.StringVar(&project, "project", "", "project identity")
	var operation command
	switch args[0] {
	case "observe":
		operation = trackingObservationCommand(flags, &owner, &project, load)
	case "mode":
		key := trackingKeyFlags(flags, &owner, &project)
		mode := flags.String("mode", "", "active, muted, stopped, or taken_over")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			item, err := l.SetTrackingMode(ctx, key(), tracking.Mode(*mode))
			if err != nil {
				return err
			}
			return printJSON(item)
		}
	case "status":
		key := trackingKeyFlags(flags, &owner, &project)
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			item, err := l.TrackingStatus(ctx, key())
			if err != nil {
				return err
			}
			return printJSON(item)
		}
	case "history":
		key := trackingKeyFlags(flags, &owner, &project)
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			history, err := l.TrackingHistory(ctx, key())
			if err != nil {
				return err
			}
			return printJSON(history)
		}
	case "feed":
		kind := flags.String("kind", "", "filter by immediate or digest")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			feed, err := l.TrackingFeed(ctx, owner, project, tracking.Disposition(*kind))
			if err != nil {
				return err
			}
			return printJSON(feed)
		}
	case "ack":
		seq := flags.Uint64("seq", 0, "sequence number from the feed")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			if *seq == 0 {
				return errors.New("track ack requires a positive -seq")
			}
			record, err := l.AcknowledgeTracking(ctx, owner, project, *seq)
			if err != nil {
				return err
			}
			return printJSON(record)
		}
	default:
		return fmt.Errorf("unknown track command %q. Run 'stead track -h' for usage", args[0])
	}
	return runTrackingCommand(ctx, flags, args[1:], operation)
}

func runPreferences(ctx context.Context, args []string) error {
	if helpRequested(args) {
		fmt.Print(preferencesHelp)
		return nil
	}
	flags := flag.NewFlagSet("preference "+args[0], flag.ContinueOnError)
	var operation command
	switch args[0] {
	case "set":
		operation = setPreferenceCommand(flags)
	case "resolve":
		var scope preferences.Context
		flags.StringVar(&scope.Owner, "owner", "", "owner identity")
		flags.StringVar(&scope.Project, "project", "", "optional project identity")
		flags.StringVar(&scope.Task, "task", "", "optional task identity")
		name := flags.String("name", ledger.TrackingNotificationPreference, "preference name")
		when := flags.String("at", "", "optional evaluation time, RFC3339")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			at, err := parseTrackingTime(*when)
			if err != nil {
				return err
			}
			record, found, err := l.ResolvePreference(ctx, scope, *name, at)
			if err != nil {
				return err
			}
			if !found {
				return printJSON(nil)
			}
			return printJSON(record)
		}
	case "revoke":
		owner := flags.String("owner", "", "owner identity")
		id := flags.String("id", "", "preference ID")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			if *owner == "" || *id == "" {
				return errors.New("preference revoke requires -owner and -id")
			}
			record, err := l.RevokePreference(ctx, *owner, *id)
			if err != nil {
				return err
			}
			return printJSON(record)
		}
	default:
		return fmt.Errorf("unknown preference command %q. Run 'stead preference -h' for usage", args[0])
	}
	return runTrackingCommand(ctx, flags, args[1:], operation)
}

func helpRequested(args []string) bool {
	return len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help"))
}

func runTrackingCommand(ctx context.Context, flags *flag.FlagSet, args []string, operation command) error {
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return withLedger(ctx, operation)
}

func trackingKeyFlags(flags *flag.FlagSet, owner, project *string) func() tracking.Key {
	source := flags.String("source", "", "stable source instance")
	object := flags.String("object", "", "stable source object ID")
	return func() tracking.Key {
		return tracking.Key{Owner: *owner, Project: *project, Source: *source, ObjectID: *object}
	}
}

func trackingObservationCommand(flags *flag.FlagSet, owner, project *string, load trackingPolicyLoader) command {
	key := trackingKeyFlags(flags, owner, project)
	var event tracking.Observation
	flags.StringVar(&event.EventID, "event-id", "", "stable source event ID")
	flags.Uint64Var(&event.Revision, "revision", 0, "positive source revision number")
	flags.StringVar(&event.Title, "title", "", "current title")
	flags.StringVar(&event.Summary, "summary", "", "summary of this revision")
	flags.BoolVar(&event.Material, "material", false, "revision changes content materially")
	flags.BoolVar(&event.Withdrawn, "withdrawn", false, "object has been withdrawn")
	flags.Func("conclusion", "affected conclusion ID (repeatable)", func(id string) error {
		event.ConclusionIDs = append(event.ConclusionIDs, id)
		return nil
	})
	task := flags.String("task", "", "optional task identity for preference resolution")
	return func(ctx context.Context, l *ledger.Ledger) error {
		event.Key = key()
		record, err := observeWithPolicy(ctx, l, event, *task, load)
		if err != nil {
			return err
		}
		return printJSON(record)
	}
}

func setPreferenceCommand(flags *flag.FlagSet) command {
	var r preferences.Record
	flags.StringVar(&r.ID, "id", "", "stable preference ID (generated if omitted)")
	flags.StringVar(&r.Owner, "owner", "", "owner identity")
	flags.StringVar(&r.Project, "project", "", "project identity for project or task scope")
	flags.StringVar(&r.Task, "task", "", "task identity for task scope")
	flags.StringVar(&r.Name, "name", ledger.TrackingNotificationPreference, "preference name")
	flags.StringVar(&r.Value, "value", "", "value (tracking.notification supports immediate, digest, or silent)")
	flags.StringVar(&r.Original, "text", "", "original user statement")
	flags.StringVar(&r.Source, "source", "local", "source of the user statement")
	scope := flags.String("scope", string(preferences.OwnerScope), "owner, project, or task")
	effective := flags.String("effective-at", "", "optional start time, RFC3339")
	expires := flags.String("expires-at", "", "optional expiry time, RFC3339")
	ttl := flags.Duration("ttl", 0, "optional validity period from the start")
	return func(ctx context.Context, l *ledger.Ledger) error {
		if *ttl < 0 || (*expires != "" && *ttl != 0) {
			return errors.New("preference expiry requires either -expires-at or a positive -ttl")
		}
		start, err := parseTrackingTime(*effective)
		if err != nil {
			return err
		}
		end, err := parseTrackingTime(*expires)
		if err != nil {
			return err
		}
		if start.IsZero() {
			start = time.Now().UTC()
		}
		if *ttl > 0 {
			end = start.Add(*ttl)
		}
		if r.ID == "" {
			r.ID = rand.Text()
		}
		r.Scope, r.EffectiveAt = preferences.Scope(*scope), start
		if !end.IsZero() {
			r.ExpiresAt = &end
		}
		record, err := l.SetPreference(ctx, r)
		if err != nil {
			return err
		}
		return printJSON(record)
	}
}

func parseTrackingTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid RFC3339 timestamp %q: %w", value, err)
	}
	return at.UTC(), nil
}
