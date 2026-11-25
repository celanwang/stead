package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
)

const localSource = "local"

func submitCommand(flags *flag.FlagSet) command {
	id := flags.String("id", "", "delegation ID (generated if omitted)")
	owner := flags.String("owner", "", "delegation owner")
	goal := flags.String("goal", "", "scheduling goal")
	target := flags.String("target", "", "destination for the scheduling action")
	content := flags.String("content", "", "exact content to be approved")
	ttl := flags.Duration("ttl", time.Hour, "time until the delegation deadline")
	key := flags.String("resource", "", "optional shared resource key")
	start := flags.String("starts", "", "resource interval start, RFC3339")
	end := flags.String("ends", "", "resource interval end, RFC3339")
	return func(ctx context.Context, l *ledger.Ledger) error {
		if *id == "" {
			*id = rand.Text()
		}
		request := delegation.Scope{Target: *target, Content: *content}
		if *key != "" || *start != "" || *end != "" {
			from, startErr := time.Parse(time.RFC3339Nano, *start)
			to, endErr := time.Parse(time.RFC3339Nano, *end)
			if *key == "" || startErr != nil || endErr != nil {
				return errors.New("a resource interval requires -resource, -starts, and -ends")
			}
			request.Resources = []delegation.Resource{{Key: *key, Start: from, End: to}}
		}
		d := delegation.Delegation{
			ID: *id, Owner: *owner, Sources: []string{localSource}, Goal: *goal, Request: request,
			Deadline: time.Now().UTC().Add(*ttl),
		}
		if err := l.Create(ctx, d); err != nil {
			return err
		}
		fmt.Println(d.ID)
		return nil
	}
}

func eventCommand(flags *flag.FlagSet) command {
	id := flags.String("id", "", "delegation ID")
	eventID := flags.String("event-id", "", "stable ID for this event")
	sender := flags.String("sender", "", "sender identity")
	kind := flags.String("kind", "", "input, approval, revoke, or evidence")
	text := flags.String("text", "", "original message or completion evidence")
	correlation := flags.String("correlation", "", "approval wait correlation from status")
	expires := flags.Duration("valid-for", 15*time.Minute, "approval validity")
	grantID := flags.String("grant", "", "grant ID to revoke")
	return func(ctx context.Context, l *ledger.Ledger) error {
		event := delegation.Event{
			Source: localSource, ExternalID: *eventID, DelegationID: *id,
			Sender: *sender, Kind: delegation.EventKind(*kind), Correlation: *id,
			Raw: *text, MayAffectGrant: *kind == string(delegation.Input), RevokeGrantID: *grantID,
		}
		if event.Kind == delegation.Approval {
			if *correlation == "" || *expires <= 0 {
				return errors.New("approval requires -correlation and a positive -valid-for")
			}
			status, err := l.Status(ctx, *id)
			if err != nil {
				return err
			}
			event.Correlation = *correlation
			event.Grant = &delegation.GrantTerms{
				Scope: status.Delegation.Request, ExpiresAt: time.Now().UTC().Add(*expires),
			}
		}
		inserted, err := l.Receive(ctx, event)
		if err != nil {
			return err
		}
		if inserted {
			fmt.Println("Event recorded.")
		} else {
			fmt.Println("Event already recorded.")
		}
		return nil
	}
}

func settleCommand(flags *flag.FlagSet) command {
	id := flags.String("action", "", "stable action ID")
	state := flags.String("state", "", "verified outcome: delivered or failed")
	detail := flags.String("detail", "", "evidence supporting this outcome")
	return func(ctx context.Context, l *ledger.Ledger) error {
		r := delegation.Receipt{ActionID: *id, State: delegation.ActionState(*state), Detail: *detail}
		if !r.Valid() || r.State == delegation.Unknown {
			return errors.New("settle requires -action, -state delivered or failed, and -detail")
		}
		return l.Reconcile(ctx, r)
	}
}
