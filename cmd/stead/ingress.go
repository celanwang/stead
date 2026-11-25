package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/celanwang/stead/internal/channels/ingress"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/rsi"
	"github.com/celanwang/stead/internal/scenarios/tracking"
)

func runIngress(ctx context.Context, args []string, load trackingPolicyLoader) error {
	flags := flag.NewFlagSet("ingress", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:8787", "numeric loopback address and port")
	var binding ingress.Binding
	flags.StringVar(&binding.Owner, "owner", "", "fixed owner identity")
	flags.StringVar(&binding.Project, "project", "", "fixed project identity")
	flags.StringVar(&binding.Source, "source", "", "fixed source instance for this collector")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return withLedger(ctx, func(ctx context.Context, l *ledger.Ledger) error {
		handler, err := ingress.NewHandler(binding, os.Getenv("STEAD_INGRESS_TOKEN"), func(ctx context.Context, event tracking.Observation) (ingress.Receipt, error) {
			intake, err := observeWithPolicy(ctx, l, event, "", load)
			if err != nil {
				return ingress.Receipt{}, err
			}
			record := intake.Record
			if intake.Duplicate && (record.Observation.Key != event.Key || record.Observation.Revision != event.Revision) {
				return ingress.Receipt{}, ingress.ErrConflict
			}
			return ingress.Receipt{
				Sequence: record.Seq, Duplicate: intake.Duplicate, Outcome: record.Decision.Outcome, Disposition: record.Decision.Disposition,
			}, nil
		})
		if err != nil {
			return err
		}
		listener, err := ingress.Listen(*listen)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Listening on http://%s/observations\n", listener.Addr())
		return ingress.Serve(ctx, listener, handler)
	})
}

func observeWithPolicy(ctx context.Context, l *ledger.Ledger, event tracking.Observation, task string, load trackingPolicyLoader) (ledger.TrackingIntake, error) {
	if err := event.Key.Validate(); err != nil {
		return ledger.TrackingIntake{}, err
	}
	policy, version := rsi.DefaultPolicy(), "baseline"
	if load != nil {
		var err error
		policy, version, err = load(ctx, l, event.Key.Owner, event.Key.Project)
		if err != nil {
			return ledger.TrackingIntake{}, err
		}
	}
	if err := policy.Validate(); err != nil {
		return ledger.TrackingIntake{}, err
	}
	return l.ObserveTracking(ctx, event, ledger.TrackingOptions{
		TaskID: task, PolicyVersion: version,
		Classify: func(f tracking.Facts) tracking.Disposition {
			return tracking.Disposition(policy.Decide(ledger.TrackingSignal(f)))
		},
	})
}
