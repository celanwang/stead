package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
)

const usage = `Stead is a personal agent harness for managing delegated tasks.

Usage: stead <command> [options]

  migrate     Apply the MySQL schema migrations.
  serve       Run the authenticated HTTP API.
  worker      Run the background worker with the local providers.
  draft       Interpret a request or validate an offline draft for review.
  submit      Submit a scheduling delegation.
  briefing    Prepare and deliver a reviewed briefing with deadline fallback.
  track       Track source revisions and read the notification feed.
  ingress     Receive source observations from a trusted local collector.
  preference  Manage scoped and temporary user preferences.
  rsi         Evaluate, improve, and activate versioned tracking policies.
  event       Record an input, approval, revocation, or completion evidence.
  list        List owner delegations and their current state.
  status      Inspect a delegation and its ledger records.
  settle      Record a verified outcome for an action awaiting verification.
  replay      Reconstruct recorded decisions and receipts offline.

Run 'stead <command> -h' for command options. Set STEAD_MYSQL_DSN for ledger access.
`

type command func(context.Context, *ledger.Ledger) error

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:])
	case "replay":
		return runReplay(ctx, args[1:])
	case "briefing":
		return runBriefing(ctx, args[1:])
	case "draft":
		return runDraft(ctx, args[1:])
	case "track":
		return runTracking(ctx, args[1:], notificationPolicy)
	case "ingress":
		return runIngress(ctx, args[1:], notificationPolicy)
	case "preference":
		return runPreferences(ctx, args[1:])
	case "rsi":
		return runRSI(ctx, args[1:])
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	var operation command
	switch args[0] {
	case "migrate":
		operation = func(ctx context.Context, l *ledger.Ledger) error { return l.Migrate(ctx) }
	case "worker":
		operation = workerCommand(flags)
	case "submit":
		operation = submitCommand(flags)
	case "event":
		operation = eventCommand(flags)
	case "list":
		owner := flags.String("owner", "", "delegation owner")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			items, err := l.List(ctx, *owner)
			if err != nil {
				return err
			}
			return printJSON(items)
		}
	case "status":
		id := flags.String("id", "", "delegation ID")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			status, err := l.Status(ctx, *id)
			if err != nil {
				return err
			}
			return printJSON(status)
		}
	case "settle":
		operation = settleCommand(flags)
	default:
		return fmt.Errorf("unknown command %q. Run 'stead --help' for usage", args[0])
	}
	if err := parseFlags(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return withLedger(ctx, operation)
}

func withLedger(ctx context.Context, operation command) error {
	l, err := ledger.Open(ctx, os.Getenv("STEAD_MYSQL_DSN"), delegation.WallClock{})
	if err != nil {
		return err
	}
	defer l.Close()
	return operation(ctx, l)
}

func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	return nil
}

func printJSON(v any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
