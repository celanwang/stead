package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/rsi"
)

const rsiHelp = `Usage: stead rsi <command> [options]

  example          Export synthetic study data, initial rules, or holdout cases.
  study            Compare fixed, simple-search, and recursive methods offline.
  init             Start a study for an owner and project.
  status           Show study state, candidate history, and live release history.
  feedback         Label a tracking decision as study evidence.
  export-feedback  Export reviewed feedback features for this project.
  improve          Run one study round using a labeled development and validation set.
  activate         Release a policy version after checking an independent holdout.
  rollback         Restore study versions or an approved live policy.

Run 'stead rsi <command> -h' for options.
`

func notificationPolicy(ctx context.Context, l *ledger.Ledger, owner, project string) (rsi.Policy, string, error) {
	record, err := l.RSIProject(ctx, owner, project)
	if errors.Is(err, delegation.ErrNotFound) {
		return rsi.DefaultPolicy(), rsi.BaselineVersion, nil
	}
	if err != nil {
		return rsi.Policy{}, "", err
	}
	return record.NotificationPolicy()
}

func runRSI(ctx context.Context, args []string) error {
	if helpRequested(args) {
		fmt.Print(rsiHelp)
		return nil
	}
	flags := flag.NewFlagSet("rsi "+args[0], flag.ContinueOnError)
	if args[0] == "example" || args[0] == "study" {
		return runRSIOffline(flags, args[0], args[1:])
	}
	var owner, project string
	flags.StringVar(&owner, "owner", "", "owner identity")
	flags.StringVar(&project, "project", "", "project identity")
	var operation command
	switch args[0] {
	case "init":
		initial := flags.String("initial", "", "optional initial research policy JSON file")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			policy := rsi.DefaultPolicy()
			if *initial != "" {
				policy = rsi.Policy{}
				if err := readRSIJSON(*initial, &policy); err != nil {
					return err
				}
			}
			record, err := l.CreateRSIProject(ctx, owner, project, policy)
			if err != nil {
				return err
			}
			return printJSON(record)
		}
	case "status":
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			record, err := l.RSIProject(ctx, owner, project)
			if err != nil {
				return err
			}
			return printJSON(record)
		}
	case "feedback":
		seq := flags.Uint64("seq", 0, "tracking observation sequence")
		expected := flags.String("expected", "", "reviewed label, immediate or digest")
		pair := flags.String("pair", "", "optional identifier shared by counterexamples")
		anchor := flags.Bool("anchor", false, "register a paired example without targeting a correction")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			label, err := l.RecordRSIFeedback(ctx, owner, project, *seq, rsi.Disposition(*expected), *pair, *anchor)
			if err != nil {
				return err
			}
			return printJSON(label)
		}
	case "export-feedback":
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			record, err := l.RSIProject(ctx, owner, project)
			if err != nil {
				return err
			}
			examples := make([]rsi.Example, 0, len(record.Feedback))
			for _, feedback := range record.Feedback {
				examples = append(examples, feedback.Example)
			}
			return printJSON(examples)
		}
	case "improve":
		path := flags.String("dataset", "", "JSON file containing development and validation examples")
		feedback := flags.Bool("include-feedback", false, "include this project's reviewed feedback in development")
		var options rsi.Options
		flags.IntVar(&options.CandidateBudget, "budget", 10, "candidate evaluations per round")
		flags.StringVar(&options.TargetID, "target", "", "optional specific development correction ID")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			var dataset rsi.Dataset
			if err := readRSIJSON(*path, &dataset); err != nil {
				return err
			}
			round, err := l.ImproveRSI(ctx, owner, project, dataset, options, *feedback)
			if err != nil {
				return err
			}
			return printJSON(round)
		}
	case "activate":
		policy := flags.String("policy", "", "study policy version to release")
		path := flags.String("holdout", "", "JSON array of independently labeled holdout cases")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			if *policy == "" {
				return errors.New("rsi activate requires -policy")
			}
			var holdout []rsi.Example
			if err := readRSIJSON(*path, &holdout); err != nil {
				return err
			}
			release, err := l.ActivateRSI(ctx, owner, project, *policy, holdout)
			if err != nil {
				return err
			}
			if err := printJSON(release); err != nil {
				return err
			}
			if !release.Report.Approved {
				return errors.New("candidate failed the live activation checks")
			}
			return nil
		}
	case "rollback":
		policy := flags.String("policy", "", "prior policy version (use baseline for live defaults)")
		improver := flags.String("improver", "", "prior improver version in the study")
		live := flags.Bool("live", false, "restore a previously approved live policy")
		operation = func(ctx context.Context, l *ledger.Ledger) error {
			record, err := l.RollbackRSI(ctx, owner, project, *policy, *improver, *live)
			if err != nil {
				return err
			}
			return printJSON(record)
		}
	default:
		return fmt.Errorf("unknown rsi command %q. Run 'stead rsi -h' for usage", args[0])
	}
	if err := parseFlags(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return withLedger(ctx, operation)
}

func runRSIOffline(flags *flag.FlagSet, name string, args []string) error {
	dataset, policy := rsi.ExampleDataset()
	var operation func() error
	switch name {
	case "example":
		part := flags.String("part", "dataset", "dataset, selection, initial, or holdout")
		operation = func() error {
			switch *part {
			case "dataset":
				return printJSON(dataset)
			case "selection":
				dataset.Holdout = nil
				return printJSON(dataset)
			case "initial":
				return printJSON(policy)
			case "holdout":
				return printJSON(dataset.Holdout)
			default:
				return errors.New("example part must be dataset, selection, initial, or holdout")
			}
		}
	case "study":
		path := flags.String("dataset", "", "labeled dataset JSON, defaults to the synthetic study")
		initial := flags.String("initial", "", "starting policy JSON")
		var opts rsi.Options
		flags.IntVar(&opts.Rounds, "rounds", 3, "rounds per study arm")
		flags.IntVar(&opts.CandidateBudget, "budget", 10, "candidate evaluations per round")
		flags.Uint64Var(&opts.Seed, "seed", 27, "seed for the simple-search arm")
		operation = func() error {
			if *path != "" {
				dataset = rsi.Dataset{}
				if err := readRSIJSON(*path, &dataset); err != nil {
					return err
				}
				policy = rsi.DefaultPolicy()
			}
			if *initial != "" {
				policy = rsi.Policy{}
				if err := readRSIJSON(*initial, &policy); err != nil {
					return err
				}
			}
			comparison, err := rsi.Compare(dataset, policy, opts)
			if err != nil {
				return err
			}
			return printJSON(comparison)
		}
	}
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return operation()
}

func readRSIJSON(path string, target any) error {
	if path == "" {
		return errors.New("an input JSON file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("read %s: expected one JSON value", path)
	}
	return nil
}
