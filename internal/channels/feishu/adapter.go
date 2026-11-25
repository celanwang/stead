// Package feishu translates verified channel messages and receipts at the kernel boundary.
package feishu

import (
	"errors"

	"github.com/celanwang/stead/internal/delegation"
)

// Binding links a reply to a recorded prompt and maps the owner's channel identity.
type Binding struct {
	ChatID       string
	DelegationID string
	Correlation  string
	OwnerOpenID  string
	Owner        string
}

type Adapter struct {
	Instance string
	// Bindings are keyed by the original prompt's Feishu message ID.
	Bindings map[string]Binding
}

// Message is supplied by a transport after verifying the event and decoding its payload.
type Message struct {
	EventID  string
	ChatID   string
	ReplyTo  string
	OpenID   string
	Kind     delegation.EventKind
	Raw      string
	Grant    *delegation.GrantTerms
	RevokeID string
}

func (a Adapter) Receive(m Message) (delegation.Event, error) {
	b, ok := a.Bindings[m.ReplyTo]
	if !ok || a.Instance == "" || b.ChatID == "" || b.DelegationID == "" ||
		b.OwnerOpenID == "" || b.Owner == "" || m.ChatID != b.ChatID ||
		m.EventID == "" || m.OpenID == "" || m.Raw == "" {
		return delegation.Event{}, errors.New("message does not match a known Feishu prompt")
	}
	source := "feishu:" + a.Instance
	sender := source + ":" + m.OpenID
	if m.OpenID == b.OwnerOpenID {
		sender = b.Owner
	} else if m.Kind != delegation.Input {
		return delegation.Event{}, errors.New("message requires the bound owner")
	}
	correlation := b.Correlation
	if m.Kind == delegation.Evidence || m.Kind == delegation.Revoke {
		correlation = b.DelegationID
	}
	return delegation.Event{
		Source: source, ExternalID: m.EventID, DelegationID: b.DelegationID,
		Sender: sender, Correlation: correlation, Raw: m.Raw, Kind: m.Kind,
		Grant: m.Grant, RevokeGrantID: m.RevokeID,
		MayAffectGrant: m.Kind == delegation.Input && m.OpenID == b.OwnerOpenID,
	}, nil
}

type Outgoing struct {
	ActionID string
	ChatID   string
	Content  string
}

type DeliveryReport struct {
	ActionID  string
	ChatID    string
	MessageID string
	State     delegation.ActionState
	Detail    string
}

func Prepare(a delegation.Action) (Outgoing, error) {
	if a.State != delegation.Sending || a.ID == "" || a.Scope.Target == "" || a.Scope.Content == "" {
		return Outgoing{}, errors.New("Feishu requires a claimed action with a target and content")
	}
	return Outgoing{ActionID: a.ID, ChatID: a.Scope.Target, Content: a.Scope.Content}, nil
}

func Receipt(request Outgoing, report DeliveryReport) (delegation.Receipt, error) {
	if request.ActionID != report.ActionID || request.ChatID == "" || request.ChatID != report.ChatID {
		return delegation.Receipt{}, errors.New("Feishu receipt does not match the action or chat")
	}
	detail := report.Detail
	if report.State == delegation.Delivered {
		detail = report.MessageID
	}
	r := delegation.Receipt{ActionID: report.ActionID, State: report.State, Detail: detail}
	if !r.Valid() {
		return delegation.Receipt{}, errors.New("invalid Feishu delivery report")
	}
	return r, nil
}
