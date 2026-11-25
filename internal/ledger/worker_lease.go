package ledger

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// WithWorkerLedger runs a worker with its ledger bound to the session that owns
// the worker lock. Losing that session makes the ledger unusable and prevents
// automatic reconnection without a new lease.
func (l *Ledger) WithWorkerLedger(ctx context.Context, run func(worker *Ledger, probe func(context.Context) error) error) error {
	return l.connectionLock(ctx, "worker", 0, func(conn *sql.Conn) error {
		// The session context makes GORM clone the statement before we pin it.
		db := l.db.Session(&gorm.Session{Context: ctx, NewDB: true})
		db.Statement.ConnPool, db.Config.ConnPool = conn, conn
		return run(&Ledger{db: db, clock: l.clock}, conn.PingContext)
	})
}
