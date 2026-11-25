package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/simulation"
)

func runReplay(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	id := flags.String("id", "", "delegation ID to read from the ledger")
	file := flags.String("file", "", "replay exported records without a ledger connection")
	export := flags.Bool("export", false, "write audit records for -id to stdout")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *file != "" {
		if *id != "" || *export {
			return errors.New("replay -file takes no -id or -export")
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		var records []delegation.Record
		if err := json.Unmarshal(data, &records); err != nil {
			return err
		}
		return replay(records)
	}
	if *id == "" {
		return errors.New("replay requires -id or -file")
	}
	return withLedger(ctx, func(ctx context.Context, l *ledger.Ledger) error {
		records, err := l.History(ctx, *id)
		if err != nil {
			return err
		}
		if *export {
			return printJSON(records)
		}
		return replay(records)
	})
}

func replay(records []delegation.Record) error {
	result, err := simulation.Replay(records)
	if err != nil {
		return err
	}
	return printJSON(result)
}
