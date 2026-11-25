package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/celanwang/stead/internal/delegation"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Ledger struct {
	db    *gorm.DB
	clock delegation.Clock
}

func Open(ctx context.Context, dsn string, clock delegation.Clock) (*Ledger, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil || cfg.DBName == "" {
		return nil, errors.New("configure a valid MySQL DSN with a database name")
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.MultiStatements = false
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 45 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	connector, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(24)
	pool.SetMaxIdleConns(8)
	pool.SetConnMaxLifetime(15 * time.Minute)
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{
		SkipDefaultTransaction: true, DisableAutomaticPing: true,
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err == nil {
		err = pool.PingContext(ctx)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("open MySQL ledger: %w", err)
	}
	return &Ledger{db: db, clock: clock}, nil
}

func (l *Ledger) CheckReady(ctx context.Context) error {
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	var applied []struct{ Version, Checksum string }
	if err := l.db.WithContext(ctx).Raw("SELECT version, checksum FROM stead_schema_migrations").Scan(&applied).Error; err != nil {
		return err
	}
	checksums := make(map[string]string, len(applied))
	for _, row := range applied {
		checksums[row.Version] = row.Checksum
	}
	for _, path := range files {
		body, err := migrations.ReadFile(path)
		if err != nil {
			return err
		}
		if checksums[path] != fmt.Sprintf("%x", sha256.Sum256(body)) {
			return fmt.Errorf("migration %s is missing or differs", path)
		}
	}
	return nil
}

func (l *Ledger) Close() error {
	db, err := l.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (l *Ledger) now() time.Time { return l.clock.Now().UTC().Truncate(time.Microsecond) }

func (l *Ledger) transaction(ctx context.Context, run func(*gorm.DB) error) error {
	err := l.db.WithContext(ctx).Transaction(run, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return delegation.ErrNotFound
	}
	return err
}

// connectionLock keeps a MySQL named lock on its physical connection for run.
func (l *Ledger) connectionLock(ctx context.Context, purpose string, seconds int, run func(*sql.Conn) error) (err error) {
	db, err := l.db.DB()
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var database string
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(database))
	name := fmt.Sprintf("stead:%s:%x", purpose, hash[:16])
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, seconds).Scan(&acquired); err != nil {
		// Discard the connection if acquisition may have succeeded on the server.
		conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fmt.Errorf("ledger %s lock is already held or unavailable", purpose)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		releaseErr := conn.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK(?)", name).Scan(&released)
		if releaseErr != nil || !released.Valid || released.Int64 != 1 {
			conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, fmt.Errorf("could not confirm release of ledger %s lock", purpose), releaseErr)
		}
	}()
	return run(conn)
}

func (l *Ledger) WithWorker(ctx context.Context, run func() error) error {
	return l.connectionLock(ctx, "worker", 0, func(*sql.Conn) error { return run() })
}

func (l *Ledger) Migrate(ctx context.Context) error {
	return l.connectionLock(ctx, "migration", 30, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS stead_schema_migrations (
			version VARCHAR(191) PRIMARY KEY, checksum CHAR(64) NOT NULL
		) ENGINE=InnoDB`)
		if err != nil {
			return err
		}
		files, err := fs.Glob(migrations, "migrations/*.sql")
		if err != nil {
			return err
		}
		for _, path := range files {
			if err := migrateFile(ctx, conn, path); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
		return nil
	})
}

func migrateFile(ctx context.Context, conn *sql.Conn, path string) error {
	body, err := migrations.ReadFile(path)
	if err != nil {
		return err
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(body))
	var existing string
	err = conn.QueryRowContext(ctx, "SELECT checksum FROM stead_schema_migrations WHERE version = ?", path).Scan(&existing)
	switch {
	case err == nil && existing == checksum:
		return nil
	case err == nil:
		return errors.New("applied migration checksum differs")
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	// MySQL DDL commits implicitly. Each statement in the embedded files is safe to reapply.
	for _, statement := range strings.Split(string(body), ";") {
		if strings.TrimSpace(statement) != "" {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO stead_schema_migrations (version, checksum) VALUES (?, ?)", path, checksum)
	return err
}
