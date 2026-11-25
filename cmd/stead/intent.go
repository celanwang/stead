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
		d, err = readDraft(*file)
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

func readDraft(path string) (intent.Draft, error) {
	f, err := os.Open(path)
	if err != nil {
		return intent.Draft{}, err
	}
	defer f.Close()
	const maxDraft = 256 << 10
	data, err := io.ReadAll(io.LimitReader(f, maxDraft+1))
	if err != nil {
		return intent.Draft{}, err
	}
	if len(data) > maxDraft {
		return intent.Draft{}, errors.New("draft file exceeds 256 KiB")
	}
	return intent.ParseDraft(data)
}
