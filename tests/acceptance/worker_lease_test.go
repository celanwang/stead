package acceptance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/celanwang/stead/internal/delegation"
	"github.com/celanwang/stead/internal/ledger"
)

func TestWorkerLedgerFencesAfterMySQLSessionLoss(t *testing.T) {
	f := newFixture(t)
	store, replacement := f.connect(), f.connect()
	d := f.create(store, "worker-lease")
	action := f.stage(store, d)

	control, err := sql.Open("mysql", f.dsn)
	must(t, err)
	defer control.Close()

	sessionLost := errors.New("test worker observed lease loss")
	err = store.WithWorkerLedger(f.ctx, func(worker *ledger.Ledger, probe func(context.Context) error) error {
		must(t, probe(f.ctx))
		err := replacement.WithWorkerLedger(f.ctx, func(*ledger.Ledger, func(context.Context) error) error {
			t.Error("second worker acquired a held lease")
			return nil
		})
		check(t, err != nil, "second worker did not get a lock error")

		claimed, err := worker.Claim(f.ctx, action.ID)
		must(t, err)
		check(t, claimed != nil && claimed.State == delegation.Sending, "leased worker could not claim action")
		var session uint64
		// connectionLock names worker leases using the database name's SHA-256 prefix.
		must(t, control.QueryRowContext(f.ctx,
			"SELECT IS_USED_LOCK(CONCAT('stead:worker:', LEFT(SHA2(DATABASE(), 256), 32)))").Scan(&session))
		_, err = control.ExecContext(f.ctx, fmt.Sprintf("KILL CONNECTION %d", session))
		must(t, err)

		check(t, probe(f.ctx) != nil, "probe did not detect the disconnected session")
		_, err = worker.Ready(f.ctx)
		check(t, err != nil, "old worker reconnected for a repository query")
		err = worker.Settle(f.ctx, delegation.Receipt{ActionID: action.ID, State: delegation.Delivered, Detail: "stale receipt"})
		check(t, err != nil, "old worker reconnected for a transaction")
		check(t, f.status(store, d.ID).Actions[0].State == delegation.Sending,
			"attempted stale settlement changed the action or broke the client's pool")

		must(t, replacement.WithWorkerLedger(f.ctx, func(fresh *ledger.Ledger, ping func(context.Context) error) error {
			if err := ping(f.ctx); err != nil {
				return err
			}
			return fresh.Settle(f.ctx, delegation.Receipt{
				ActionID: action.ID, State: delegation.Unknown, Detail: "previous worker disconnected",
			})
		}))
		return sessionLost
	})
	check(t, errors.Is(err, sessionLost), "worker did not return its lease loss: %v", err)
	check(t, f.status(store, d.ID).Actions[0].State == delegation.Unknown,
		"fresh lease failed to settle the interrupted sending action")
}
