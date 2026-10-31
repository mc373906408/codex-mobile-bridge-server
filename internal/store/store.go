package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/security"
)

//go:embed migrations/*.sql
var migrations embed.FS

var ErrDenied = errors.New("access denied")
var ErrConflict = errors.New("state conflict")
var ErrNotFound = errors.New("not found")

type Store struct {
	Pool  *pgxpool.Pool
	Vault *security.Vault
}

func Interval(d time.Duration) string { return fmt.Sprintf("%d seconds", int64(d.Seconds())) }

func Open(ctx context.Context, databaseURL string, key []byte) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	config.MaxConns = 24
	config.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	vault, err := security.NewVault(key)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool, Vault: vault}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(607843092)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
			return err
		}
		files, err := migrations.ReadDir("migrations")
		if err != nil {
			return err
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
		for _, file := range files {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", file.Name()).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			data, err := migrations.ReadFile("migrations/" + file.Name())
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, string(data)); err != nil {
				return fmt.Errorf("migration %s: %w", file.Name(), err)
			}
			if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES($1)", file.Name()); err != nil {
				return err
			}
		}
		return nil
	})
}

func Audit(ctx context.Context, tx pgx.Tx, actor, action, target string) error {
	var targetID any
	if target != "" {
		targetID = target
	}
	_, err := tx.Exec(ctx, "INSERT INTO audit_log(actor_id,action,target_id) VALUES($1,$2,$3)", actor, action, targetID)
	return err
}
