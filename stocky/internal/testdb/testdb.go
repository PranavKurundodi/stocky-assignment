// Package testdb gives integration tests a migrated, empty Postgres database.
//
// Tests run against TEST_DATABASE_URL (e.g. the assignment_test database).
// When it is unset, Pool skips the calling test with a clear message, so
// `go test ./...` still passes on a machine without Postgres.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
)

// lockKey is an arbitrary constant for pg_advisory_lock. `go test ./...`
// runs each package's tests as a separate process in parallel; they all share
// one database and truncate it, so each process holds this lock for its whole
// run and the packages take turns instead of wiping each other's data.
const lockKey = 7_421_000

var (
	url  string
	pool *pgxpool.Pool
)

// Main is called from a package's TestMain. It connects, takes the lock,
// migrates, runs the tests and exits.
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if err := timeutil.Init(); err != nil {
		logrus.WithError(err).Error("testdb: load IST")
		return 1
	}

	url = os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		return m.Run() // integration tests will skip themselves
	}

	ctx := context.Background()
	p, err := db.NewPool(ctx, url)
	if err != nil {
		logrus.WithError(err).Error("testdb: connect to TEST_DATABASE_URL")
		return 1
	}
	defer p.Close()

	// A session-level advisory lock belongs to one connection, so hold a
	// dedicated connection for as long as the tests run.
	lockConn, err := p.Acquire(ctx)
	if err != nil {
		logrus.WithError(err).Error("testdb: acquire lock connection")
		return 1
	}
	defer lockConn.Release()
	if _, err := lockConn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		logrus.WithError(err).Error("testdb: take advisory lock")
		return 1
	}
	defer func() { _, _ = lockConn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey) }()

	if _, err := db.MigrateUp(url); err != nil {
		logrus.WithError(err).Error("testdb: migrate")
		return 1
	}

	pool = p
	return m.Run()
}

// Pool returns the test database with every table emptied and the base
// stocks restored. It skips the test when TEST_DATABASE_URL is unset.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if pool == nil {
		t.Skip("TEST_DATABASE_URL is not set; skipping integration test. " +
			"Example: TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/assignment_test?sslmode=disable")
	}
	ctx := context.Background()
	// ResetData also removes stocks a previous test registered, so every
	// test starts from the five base stocks.
	if err := db.ResetData(ctx, pool); err != nil {
		t.Fatalf("reset data: %v", err)
	}
	return pool
}

// URL returns TEST_DATABASE_URL. Only valid after Pool has not skipped.
func URL() string {
	return url
}
