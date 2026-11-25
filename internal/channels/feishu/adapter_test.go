package feishu

import (
	"testing"

	"github.com/celanwang/stead/internal/delegation"
)

func TestLocalMessageAndReceipt(t *testing.T) {
	adapter := Adapter{Instance: "dev", Bindings: map[string]Binding{
		"prompt-a": {
			ChatID: "chat-1", DelegationID: "a", Correlation: "a/1", OwnerOpenID: "open-owner", Owner: "owner",
		},
		"prompt-b": {
			ChatID: "chat-1", DelegationID: "b", Correlation: "b/2", OwnerOpenID: "open-owner", Owner: "owner",
		},
	}}
	message := Message{EventID: "event-42", ChatID: "chat-1", ReplyTo: "prompt-b", OpenID: "open-owner", Kind: delegation.Input, Raw: "Hold the booking"}
	e, err := adapter.Receive(message)
	if err != nil || e.DelegationID != "b" || e.Correlation != "b/2" || e.Source != "feishu:dev" ||
		e.Sender != "owner" || e.ExternalID != message.EventID || !e.MayAffectGrant || e.Raw != message.Raw {
		t.Fatalf("translated event=%+v err=%v", e, err)
	}
	wrongChat := message
	wrongChat.ChatID = "chat-2"
	if _, err := adapter.Receive(wrongChat); err == nil {
		t.Fatal("accepted a reply from a different chat")
	}
	wrongOwner := message
	wrongOwner.OpenID, wrongOwner.Kind = "other-open-id", delegation.Revoke
	if _, err := adapter.Receive(wrongOwner); err == nil {
		t.Fatal("accepted a privileged event from a different sender")
	}
	action := delegation.Action{ID: "b-2", State: delegation.Sending, Scope: delegation.Scope{Target: "chat-1", Content: "Confirm the booking"}}
	outgoing, err := Prepare(action)
	if err != nil || outgoing.ActionID != action.ID || outgoing.Content != action.Scope.Content {
		t.Fatalf("outgoing request=%+v err=%v", outgoing, err)
	}
	report := DeliveryReport{ActionID: action.ID, ChatID: outgoing.ChatID, MessageID: "message-17", State: delegation.Delivered}
	receipt, err := Receipt(outgoing, report)
	if err != nil || receipt.ActionID != action.ID || receipt.State != delegation.Delivered || receipt.Detail != report.MessageID {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	report.ChatID = "chat-2"
	if _, err := Receipt(outgoing, report); err == nil {
		t.Fatal("accepted a receipt for a different chat")
	}
}
