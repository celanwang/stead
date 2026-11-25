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
	"time"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/scenarios/scheduling"
	"github.com/celanwang/stead/internal/simulation"
)

const usage = `Stead is a personal agent harness for managing delegated tasks.

Usage: stead <command> [options]

  migrate  Apply the MySQL schema migrations.
  worker   Run the background worker with the local simulator.
  submit   Submit a scheduling delegation.
  event    Record an input, approval, revocation, or completion evidence.
  status   Inspect a delegation and its ledger records.
  settle   Record a verified outcome for an action awaiting verification.
  replay   Reconstruct recorded decisions and receipts offline.

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
	if args[0] == "replay" {
		return runReplay(ctx, args[1:])
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

func workerCommand(flags *flag.FlagSet) command {
	dir := flags.String("deliveries", "", "directory for simulated deliveries")
	interval := flags.Duration("poll", 250*time.Millisecond, "ledger polling interval")
	return func(ctx context.Context, l *ledger.Ledger) error {
		if *dir == "" {
			return errors.New("worker requires -deliveries")
		}
		executor, err := simulation.NewExecutor(*dir)
		if err != nil {
			return err
		}
		runtime := delegation.Runtime{Store: l, Proposer: scheduling.Proposer{}, Executor: executor}
		return l.WithWorker(ctx, func() error {
			return runtime.Run(ctx, *interval, func(err error) { fmt.Fprintln(os.Stderr, err) })
		})
	}
}

func printJSON(v any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
