package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PranavKurundodi/stocky-assignment/stocky/migrations"
)

// BaseStocks are the stocks the migration seeds and --reset restores.
var BaseStocks = []string{"RELIANCE", "TCS", "INFY", "ICICIBANK", "HDFCBANK"}

// newMigrator builds a migrator over the embedded SQL files.
func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("init migrations: %w", err)
	}
	return m, nil
}

// migrateURL rewrites postgres://... to pgx5://..., the scheme golang-migrate
// uses for its pgx v5 driver. The rest of the URL is unchanged.
func migrateURL(databaseURL string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(databaseURL, prefix) {
			return "pgx5://" + strings.TrimPrefix(databaseURL, prefix)
		}
	}
	return databaseURL
}

// MigrateUp applies every pending migration and returns the resulting
// version. golang-migrate takes a Postgres advisory lock while it runs, so
// several instances starting at once cannot apply migrations twice.
func MigrateUp(databaseURL string) (uint, error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return 0, err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	version, dirty, err := m.Version()
	if err != nil {
		return 0, fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return version, fmt.Errorf("migration %d is dirty; fix the database by hand", version)
	}
	return version, nil
}

// MigrateDown reverts every migration. Only tests use it.
func MigrateDown(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("revert migrations: %w", err)
	}
	return nil
}

// ResetData empties every table except schema_migrations, and leaves stocks
// holding exactly the five base stocks, all ACTIVE. Stocks registered later
// (new listings from the price feed, merger targets) are removed too, so a
// reset really is a clean slate. Used by `seed --reset` and the tests.
// TRUNCATE does not fire the append-only row triggers, which is deliberate:
// wiping a dev database is allowed, editing history is not.
func ResetData(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		TRUNCATE ledger_entries, ledger_transactions, corporate_actions,
		         reward_events, stock_prices, users`)
	if err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	// Every table that references stocks was just truncated, so extra
	// stocks can be deleted without foreign key errors.
	_, err = pool.Exec(ctx, `DELETE FROM stocks WHERE NOT (symbol = ANY($1))`, BaseStocks)
	if err != nil {
		return fmt.Errorf("remove extra stocks: %w", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO stocks (symbol, status)
		SELECT unnest($1::text[]), 'ACTIVE'
		ON CONFLICT (symbol) DO UPDATE SET status = 'ACTIVE', updated_at = now()`, BaseStocks)
	if err != nil {
		return fmt.Errorf("re-seed stocks: %w", err)
	}
	return nil
}
