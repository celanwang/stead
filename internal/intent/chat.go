package intent

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Endpoint string // Complete Chat Completions endpoint.
	Model    string
	APIKey   string // Optional for a local model server.
	Timeout  time.Duration
}

type chatInterpreter struct {
	config Config
	client http.Client
}

func NewChatCompletions(cfg Config, client *http.Client) (Interpreter, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("configure a complete HTTP(S) chat completions endpoint")
	}
	if cfg.APIKey != "" && u.Scheme == "http" && u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("an API key requires an HTTPS or loopback endpoint")
	}
	if !present(cfg.Model, 512) || cfg.Timeout < 0 {
		return nil, errors.New("configure a model name and a nonnegative timeout")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	// Never forward private task text or credentials to a redirect destination.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &chatInterpreter{config: cfg, client: c}, nil
}

const instruction = `Prepare one JSON draft for its owner's review. Use the user's language. Choose exactly one shape:
{"kind":"scheduling","goal":"...","scheduling":{"target":"...","content":"..."}}
{"kind":"briefing","goal":"...","briefing":{"sources":["..."],"format":"..."}}
{"kind":"tracking","goal":"...","tracking":{"source":"...","object_id":"...","watch":"..."}}
For briefing format, use "bullets" or "plain". For tracking, use the stable object identifier given by the user. Only include facts supplied by the user. If required details are missing, the request is ambiguous, or no shape fits, return {"clarification":"one question for the owner"}. You only draft. Do not run tools, issue approvals, change settings, or add fields.`

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *chatInterpreter) Interpret(ctx context.Context, input string) (Draft, error) {
	if !present(input, 32768) {
		return Draft{}, errors.New("enter a request of 1 to 32768 bytes")
	}
	payload, err := json.Marshal(struct {
		Model          string            `json:"model"`
		Messages       []message         `json:"messages"`
		ResponseFormat map[string]string `json:"response_format"`
	}{
		Model: c.config.Model, Messages: []message{{"system", instruction}, {"user", input}},
		ResponseFormat: map[string]string{"type": "json_object"},
	})
	if err != nil {
		return Draft{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Draft{}, errors.New("invalid chat endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Draft{}, ctx.Err()
		}
		return Draft{}, errors.New("could not contact chat endpoint")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Draft{}, fmt.Errorf("chat endpoint returned HTTP %d", resp.StatusCode)
	}
	const maxResponse = 256 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return Draft{}, ctx.Err()
		}
		return Draft{}, errors.New("could not read chat response")
	}
	if len(body) > maxResponse {
		return Draft{}, errors.New("chat response exceeds size limit")
	}
	return interpretResponse(body)
}

func interpretResponse(body []byte) (Draft, error) {
	var response struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				message
				Refusal      string           `json:"refusal"`
				ToolCalls    []jsontext.Value `json:"tool_calls"`
				FunctionCall *jsontext.Value  `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Choices) != 1 {
		return Draft{}, errors.New("expected one chat completion")
	}
	choice := response.Choices[0]
	m := choice.Message
	if choice.FinishReason != "stop" || m.Role != "assistant" || m.Refusal != "" || len(m.ToolCalls) > 0 || m.FunctionCall != nil {
		return Draft{}, errors.New("chat endpoint did not produce a complete text draft")
	}
	return ParseDraft([]byte(m.Content))
}

// Clarification is a model-suggested question displayed to the owner.
type Clarification struct{ Question string }

func (c *Clarification) Error() string { return "request needs clarification: " + c.Question }

// ParseDraft validates the same strict wire format for online and offline intake.
func ParseDraft(data []byte) (Draft, error) {
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(data, &fields); err != nil {
		return Draft{}, errors.New("draft must contain a single JSON object with unique field names")
	}
	if len(fields) == 1 {
		var q string
		if err := json.Unmarshal(fields["clarification"], &q); err == nil && present(q, 1024) {
			return Draft{}, &Clarification{Question: strings.TrimSpace(q)}
		}
	}

	var d Draft
	if len(fields) != 3 || json.Unmarshal(data, &d, json.RejectUnknownMembers(true)) != nil {
		return Draft{}, errors.New("draft must contain exactly kind, goal, and the matching workflow fields")
	}
	if err := d.Validate(); err != nil {
		return Draft{}, err
	}
	return d, nil
}
