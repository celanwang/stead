package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/intent"
	"github.com/celanwang/stead/internal/ledger"
)

const briefingUsage = `Usage: stead briefing <command> [options]

  fact             Record a versioned source value.
  audience         Record a versioned audience classification.
  submit           Delegate a briefing with review and delivery conditions.
  review           Confirm a source fingerprint as the specified reviewer.
  amend            Change the recipient, format, or take over one item.
  manual-delivery  Record that the owner independently delivered the prepared briefing.
  deliveries       Inspect the reference provider's delivery records.
`

func runBriefing(ctx context.Context, args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
		fmt.Print(briefingUsage)
		return nil
	}
	flags := flag.NewFlagSet("briefing "+args[0], flag.ContinueOnError)
	var op command
	switch args[0] {
	case "fact":
		op = briefingFact(flags)
	case "audience":
		op = briefingAudience(flags)
	case "submit":
		op = briefingSubmit(flags)
	case "review", "amend":
		op = briefingEvent(flags, args[0])
	case "manual-delivery", "deliveries":
		op = briefingDeliveries(flags, args[0] == "manual-delivery")
	default:
		return fmt.Errorf("unknown briefing command %q", args[0])
	}
	if err := parseFlags(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return withLedger(ctx, op)
}

func briefingFact(flags *flag.FlagSet) command {
	owner := flags.String("owner", "", "delegation owner")
	project := flags.String("project", "", "project key")
	id := flags.String("source", "", "stable source ID")
	value := flags.String("value", "", "source value")
	shareable := flags.Bool("shareable", false, "allow this value in the briefing")
	expected := flags.Uint64("expect", 0, "expected revision (zero for creation)")
	return func(ctx context.Context, l *ledger.Ledger) error {
		fact, err := l.SetBriefingFact(ctx, *owner, *project, *id, *value, *shareable, *expected)
		if err != nil {
			return err
		}
		return printJSON(fact)
	}
}

func briefingAudience(flags *flag.FlagSet) command {
	owner := flags.String("owner", "", "delegation owner")
	project := flags.String("project", "", "project key")
	target := flags.String("target", "", "stable destination ID")
	class := flags.String("class", "", "audience classification")
	expected := flags.Uint64("expect", 0, "expected revision (zero for creation)")
	return func(ctx context.Context, l *ledger.Ledger) error {
		group, err := l.SetBriefingAudience(ctx, *owner, *project, *target, *class, *expected)
		if err != nil {
			return err
		}
		return printJSON(group)
	}
}

func briefingSubmit(flags *flag.FlagSet) command {
	file := flags.String("draft", "", "reviewed briefing draft JSON file")
	owner := flags.String("owner", "", "delegation owner")
	project := flags.String("project", "", "project key")
	reviewer := flags.String("reviewer", "", "specified reviewer principal")
	target := flags.String("target", "", "stable destination ID")
	audience := flags.String("audience", "internal", "required audience classification")
	sources := flags.String("sources", "", "comma-separated stable source IDs")
	format := flags.String("format", "", "bullets or plain (default bullets)")
	goal := flags.String("goal", "", "briefing goal")
	ttl := flags.Duration("ttl", time.Hour, "time until the delegation deadline")
	lead := flags.Duration("ask-lead", 15*time.Minute, "latest intervention lead time when there is no fallback")
	fallback := flags.Bool("fallback", true, "return the prepared draft to the owner at deadline")
	return func(ctx context.Context, l *ledger.Ledger) error {
		var ids []string
		if *file != "" {
			draft, err := readDraft(*file)
			if err != nil {
				return err
			}
			if draft.Kind != intent.Briefing {
				return errors.New("briefing submit requires a briefing draft")
			}
			if *goal == "" {
				*goal = draft.Goal
			}
			ids = draft.Briefing.Sources
			if *format == "" {
				*format = draft.Briefing.Format
			}
		}
		if *format == "" {
			*format = "bullets"
		}
		if *sources != "" {
			ids = strings.Split(*sources, ",")
		}
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
		}
		b, err := delegation.NewBriefing(*project, *reviewer, *audience, *format, ids, *fallback, *lead)
		if err != nil {
			return err
		}
		d := delegation.Delegation{
			ID: rand.Text(), Owner: *owner, Goal: *goal, Sources: []string{localSource},
			Deadline: time.Now().UTC().Add(*ttl), Request: delegation.Scope{Target: *target},
			Briefing: b,
		}
		if err := l.Create(ctx, d); err != nil {
			return err
		}
		fmt.Println(d.ID)
		return nil
	}
}

func briefingEvent(flags *flag.FlagSet, kind string) command {
	id := flags.String("id", "", "delegation ID")
	eventID := flags.String("event-id", "", "stable identifier for this event")
	sender := flags.String("sender", "", "event sender")
	raw := flags.String("text", "", "original review or instruction")
	var review delegation.ReviewNote
	var amendment delegation.Amendment
	if kind == "review" {
		flags.StringVar(&review.Source, "source", "", "source ID")
		flags.StringVar(&review.Fingerprint, "fingerprint", "", "reviewed source fingerprint")
	} else {
		flags.StringVar(&amendment.Target, "target", "", "replacement destination")
		flags.StringVar(&amendment.Format, "format", "", "replacement format")
		flags.StringVar(&amendment.TakeOver, "take-over", "", "source item for the owner to take over")
	}
	return func(ctx context.Context, l *ledger.Ledger) error {
		e := delegation.Event{
			Source: localSource, DelegationID: *id, ExternalID: *eventID, Correlation: *id, Sender: *sender, Raw: *raw,
		}
		if kind == "review" {
			e.Kind, e.Review = delegation.Review, &review
		} else {
			e.Kind, e.Amendment = delegation.Amend, &amendment
		}
		inserted, err := l.Receive(ctx, e)
		if err != nil {
			return err
		}
		return printJSON(struct{ Inserted bool }{inserted})
	}
}

func briefingDeliveries(flags *flag.FlagSet, manual bool) command {
	id := flags.String("id", "", "delegation ID")
	var deliveryID string
	var owner string
	if manual {
		flags.StringVar(&deliveryID, "delivery-id", "", "stable identifier for the independent delivery")
		flags.StringVar(&owner, "owner", "", "delegation owner")
	}
	return func(ctx context.Context, l *ledger.Ledger) error {
		if !manual {
			rows, err := l.BriefingDeliveries(ctx, *id)
			if err != nil {
				return err
			}
			return printJSON(rows)
		}
		status, err := l.Status(ctx, *id)
		if err != nil {
			return err
		}
		d := status.Delegation
		if d.Owner != owner || owner == "" || deliveryID == "" || len(deliveryID) > 180 {
			return errors.New("manual delivery requires the delegation owner and a stable delivery ID")
		}
		r, err := l.WriteBriefingDelivery(ctx, "manual:"+deliveryID, d.Request, true)
		if err != nil {
			return err
		}
		if err := printJSON(r); err != nil {
			return err
		}
		if r.State != delegation.Delivered {
			return errors.New(r.Detail)
		}
		return nil
	}
}
