package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"strings"

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
		model, configErr := configuredModel()
		if configErr != nil {
			return configErr
		}
		if model == nil {
			return errors.New("configure STEAD_MODEL_ENDPOINT and STEAD_MODEL or DASHSCOPE_API_KEY")
		}
		d, err = model.Interpret(ctx, *text)
	}
	if err != nil {
		return err
	}
	return printJSON(d)
}

func configuredModel() (intent.Interpreter, error) {
	cfg := intent.Config{
		Endpoint: os.Getenv("STEAD_MODEL_ENDPOINT"), Model: os.Getenv("STEAD_MODEL"), APIKey: os.Getenv("STEAD_MODEL_API_KEY"),
	}
	bailian := os.Getenv("DASHSCOPE_API_KEY")
	if cfg.Endpoint == "" && bailian != "" {
		cfg.Endpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	}
	if cfg.Endpoint == "" && cfg.Model == "" && cfg.APIKey == "" {
		return nil, nil
	}
	if u, err := url.Parse(cfg.Endpoint); err == nil && u.Scheme == "https" {
		host := strings.ToLower(u.Hostname())
		if host == "dashscope.aliyuncs.com" || host == "dashscope-intl.aliyuncs.com" || strings.HasSuffix(host, ".maas.aliyuncs.com") {
			if cfg.APIKey == "" {
				cfg.APIKey = bailian
			}
			if cfg.Model == "" {
				cfg.Model = "qwen-plus"
			}
		}
	}
	return intent.NewChatCompletions(cfg, nil)
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
