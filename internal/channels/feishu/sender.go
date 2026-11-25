package feishu

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/celanwang/stead/internal/delegation"
)

const TargetPrefix = "feishu:"
const tokenPath = "/open-apis/auth/v3/tenant_access_token/internal"
const sendPath = "/open-apis/im/v1/messages?receive_id_type=chat_id"

type SenderConfig struct {
	AppID          string
	AppSecret      string
	AllowedChatIDs []string
	BaseURL        string // Optional API origin. Plain HTTP is permitted only on numeric loopback.
}

type Sender struct {
	appID, secret, origin string
	allow                 map[string]bool
	client                http.Client
	// Only the holder of cacheLock may read or refresh the token.
	cacheLock chan struct{}
	token     string
	refreshAt time.Time
	now       func() time.Time
}

func NewSender(cfg SenderConfig, client *http.Client) (*Sender, error) {
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" || len(cfg.AllowedChatIDs) == 0 {
		return nil, errors.New("Feishu requires an app ID, app secret, and an explicit chat allowlist")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.feishu.cn"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, errors.New("invalid Feishu API origin")
	}
	allow := make(map[string]bool, len(cfg.AllowedChatIDs))
	for _, id := range cfg.AllowedChatIDs {
		if id == "" || strings.ContainsAny(id, ":*, \t\n\r") {
			return nil, errors.New("Feishu allowlist requires individual unqualified chat IDs")
		}
		allow[id] = true
	}
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Sender{
		appID: cfg.AppID, secret: cfg.AppSecret, origin: strings.TrimRight(cfg.BaseURL, "/"), allow: allow,
		client: c, cacheLock: make(chan struct{}, 1), now: time.Now,
	}, nil
}

func (s *Sender) post(ctx context.Context, path, token string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.origin+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid Feishu request")
	}
	req.GetBody = nil // This transport never replays a POST.
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.New("Feishu transport interrupted")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Feishu HTTP %d", resp.StatusCode)
	}
	const limit = 256 << 10
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) > limit || json.Unmarshal(data, out) != nil {
		return errors.New("invalid Feishu response")
	}
	return nil
}

func (s *Sender) tenantToken(ctx context.Context) (string, error) {
	select {
	case s.cacheLock <- struct{}{}:
		defer func() { <-s.cacheLock }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	now := s.now()
	if now.Before(s.refreshAt) {
		return s.token, nil
	}
	body, err := json.Marshal(map[string]string{"app_id": s.appID, "app_secret": s.secret})
	if err != nil {
		return "", errors.New("invalid Feishu app credentials")
	}
	var out struct {
		Code   *int   `json:"code"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := s.post(ctx, tokenPath, "", body, &out); err != nil {
		return "", err
	}
	if out.Code == nil || *out.Code != 0 || out.Token == "" || strings.ContainsAny(out.Token, " \r\n\t") || out.Expire <= 0 {
		return "", errors.New("Feishu tenant token was not issued")
	}
	ttl := time.Duration(min(out.Expire, 7200)) * time.Second
	s.token, s.refreshAt = out.Token, now.Add(ttl-min(time.Minute, ttl/10))
	return s.token, nil
}

// The provider UUID derives from the durable action ID. UUIDv8 reserves the remaining bits for this hash.
func actionUUID(id string) string {
	h := sha256.Sum256([]byte("stead/feishu/action/v1/" + id))
	h[6], h[8] = h[6]&0x0f|0x80, h[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

func (s *Sender) Execute(ctx context.Context, a delegation.Action) (delegation.Receipt, error) {
	failed := func(detail string) (delegation.Receipt, error) {
		return delegation.Receipt{ActionID: a.ID, State: delegation.Failed, Detail: detail}, nil
	}
	outgoing, err := Prepare(a)
	if err != nil {
		return failed(err.Error())
	}
	chat, ok := strings.CutPrefix(outgoing.ChatID, TargetPrefix)
	if !ok || !s.allow[chat] || a.Scope.Guard != nil || a.GrantID == "" || a.DelegationID == "" {
		return failed("Feishu requires an approved scheduling action for an allowed chat")
	}
	outgoing.ChatID = chat
	const maxMessage = 150 << 10
	if len(outgoing.Content) > maxMessage {
		return failed("Feishu message exceeds 150 KiB")
	}
	text, err := json.Marshal(map[string]string{"text": outgoing.Content})
	if err != nil {
		return failed("invalid Feishu text content")
	}
	body, err := json.Marshal(map[string]string{
		"receive_id": chat, "msg_type": "text", "content": string(text), "uuid": actionUUID(a.ID),
	})
	if err != nil || len(body) > maxMessage {
		return failed("invalid or oversized Feishu message")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, err := s.tenantToken(ctx)
	if err != nil {
		return failed("Feishu token unavailable before send: " + err.Error())
	}
	if err := ctx.Err(); err != nil {
		return failed("Feishu request canceled before send")
	}
	var out struct {
		Code *int `json:"code"`
		Data struct {
			ChatID    string `json:"chat_id"`
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	report := DeliveryReport{ActionID: a.ID, ChatID: chat, State: delegation.Unknown}
	switch err := s.post(ctx, sendPath, token, body, &out); {
	case err != nil:
		report.Detail = err.Error()
	case out.Code == nil:
		report.Detail = "Feishu response missing result code"
	case *out.Code != 0:
		report.Detail = fmt.Sprintf("Feishu send returned code %d", *out.Code)
	case out.Data.ChatID != chat || strings.TrimSpace(out.Data.MessageID) == "":
		report.Detail = "Feishu response did not identify delivery to the approved chat"
	default:
		report.State, report.MessageID = delegation.Delivered, out.Data.MessageID
	}
	return Receipt(outgoing, report)
}
