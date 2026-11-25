package main

import (
	"context"
	"errors"
	"testing"

	"github.com/celanwang/stead/internal/delegation"
)

type deliveryFunc func(context.Context, delegation.Action) (delegation.Receipt, error)

func (f deliveryFunc) Execute(ctx context.Context, a delegation.Action) (delegation.Receipt, error) {
	return f(ctx, a)
}

func TestFeishuExecutorPersistsProviderProofBeforeReturningSuccess(t *testing.T) {
	a := delegation.Action{ID: "action", Scope: delegation.Scope{Target: "feishu:oc_team"}}
	positive := delegation.Receipt{ActionID: a.ID, State: delegation.Delivered, Detail: "om_ack"}
	unavailable := errors.New("write interrupted")
	for _, tc := range []struct {
		name   string
		result delegation.Receipt
		write  error
		store  bool
		fail   bool
	}{
		{"delivered", positive, nil, true, false},
		{"acknowledgment write interrupted", positive, unavailable, true, true},
		{"different action", delegation.Receipt{ActionID: "another", State: delegation.Delivered, Detail: "om_ack"}, nil, false, true},
		{"unknown outcome", delegation.Receipt{ActionID: a.ID, State: delegation.Unknown, Detail: "interrupted"}, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stored bool
			executor := workerExecutor{
				feishu: deliveryFunc(func(context.Context, delegation.Action) (delegation.Receipt, error) { return tc.result, nil }),
				ack: func(_ context.Context, r delegation.Receipt) error {
					stored = true
					if r != positive {
						t.Fatalf("unexpected acknowledgment: %+v", r)
					}
					return tc.write
				},
			}
			receipt, err := executor.Execute(t.Context(), a)
			if (err != nil) != tc.fail || stored != tc.store || (err == nil && receipt != tc.result) {
				t.Fatalf("receipt=%+v err=%v stored=%t", receipt, err, stored)
			}
		})
	}
}
