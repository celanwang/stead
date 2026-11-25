package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/celanwang/stead/internal/channels/feishu"
	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
	"github.com/celanwang/stead/internal/scenarios/briefing"
	"github.com/celanwang/stead/internal/scenarios/scheduling"
	"github.com/celanwang/stead/internal/simulation"
)

func workerCommand(flags *flag.FlagSet) command {
	dir := flags.String("deliveries", "", "directory for simulated scheduling deliveries")
	interval := flags.Duration("poll", 250*time.Millisecond, "ledger polling interval")
	observe := flags.Duration("observe", 15*time.Second, "time between briefing provider checks")
	return func(ctx context.Context, l *ledger.Ledger) error {
		if *interval <= 0 || *observe <= 0 {
			return errors.New("worker requires positive -poll and -observe intervals")
		}
		var router workerExecutor
		if *dir != "" {
			local, err := simulation.NewExecutor(*dir)
			if err != nil {
				return err
			}
			router.local = local
		}
		app, secret, chats := os.Getenv("STEAD_FEISHU_APP_ID"), os.Getenv("STEAD_FEISHU_APP_SECRET"), os.Getenv("STEAD_FEISHU_CHATS")
		if app != "" || secret != "" || chats != "" {
			ids := strings.Split(chats, ",")
			for i := range ids {
				ids[i] = strings.TrimSpace(ids[i])
			}
			sender, err := feishu.NewSender(feishu.SenderConfig{AppID: app, AppSecret: secret, AllowedChatIDs: ids}, nil)
			if err != nil {
				return err
			}
			router.feishu = sender
		}
		if err := l.CheckReady(ctx); err != nil {
			return fmt.Errorf("worker requires an up-to-date ledger: %w", err)
		}
		return l.WithWorkerLedger(ctx, func(leased *ledger.Ledger, probe func(context.Context) error) error {
			router.briefing = briefing.Executor{World: leased, Other: router.local}
			router.ack = leased.RecordDeliveryAcknowledgment
			runtime := delegation.Runtime{
				Store: leased, Proposer: briefing.Proposer{World: leased, Poll: *observe, Other: scheduling.Proposer{}},
				Executor: router, Probe: probe,
				Verify: func(ctx context.Context) error {
					return errors.Join(leased.VerifyBriefingReceipts(ctx), leased.VerifyDeliveryAcknowledgments(ctx))
				},
			}
			return runtime.Run(ctx, *interval, func(err error) { fmt.Fprintln(os.Stderr, err) })
		})
	}
}

type workerExecutor struct {
	local    delegation.Executor
	feishu   delegation.Executor
	briefing briefing.Executor
	ack      func(context.Context, delegation.Receipt) error
}

func (w workerExecutor) Execute(ctx context.Context, a delegation.Action) (delegation.Receipt, error) {
	if strings.HasPrefix(a.Scope.Target, feishu.TargetPrefix) {
		if a.Scope.Guard != nil || w.feishu == nil {
			return delegation.Receipt{ActionID: a.ID, State: delegation.Failed, Detail: "Feishu delivery is not enabled for this action"}, nil
		}
		r, err := w.feishu.Execute(ctx, a)
		if err != nil || r.State != delegation.Delivered {
			return r, err
		}
		if r.ActionID != a.ID {
			return delegation.Receipt{}, errors.New("Feishu acknowledgment identified a different action")
		}
		if err := w.ack(ctx, r); err != nil {
			return delegation.Receipt{}, fmt.Errorf("record Feishu acknowledgment: %w", err)
		}
		return r, nil
	}
	if a.Scope.Guard != nil {
		return w.briefing.Execute(ctx, a)
	}
	if w.local == nil {
		return delegation.Receipt{ActionID: a.ID, State: delegation.Failed, Detail: "target has no configured delivery provider"}, nil
	}
	return w.local.Execute(ctx, a)
}
