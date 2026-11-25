package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/celanwang/stead/internal/intent"
)

func runDraft(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("draft", flag.ContinueOnError)
	text := flags.String("text", "", "describe the delegation to the configured model")
	file := flags.String("file", "", "validate a saved draft JSON file offline")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if (*file == "") == (*text == "") {
		return errors.New("select exactly one of -text and -file")
	}
	var d intent.Draft
	var err error
	if *file != "" {
		f, openErr := os.Open(*file)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		const maxDraft = 256 << 10
		data, readErr := io.ReadAll(io.LimitReader(f, maxDraft+1))
		if readErr != nil {
			return readErr
		}
		if len(data) > maxDraft {
			return errors.New("draft file exceeds 256 KiB")
		}
		d, err = intent.ParseDraft(data)
	} else {
		model, configErr := intent.NewChatCompletions(intent.Config{
			Endpoint: os.Getenv("STEAD_MODEL_ENDPOINT"),
			Model:    os.Getenv("STEAD_MODEL"),
			APIKey:   os.Getenv("STEAD_MODEL_API_KEY"),
		}, nil)
		if configErr != nil {
			return configErr
		}
		d, err = model.Interpret(ctx, *text)
	}
	if err != nil {
		return err
	}
	return printJSON(d)
}
