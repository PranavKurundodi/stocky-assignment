# Repo conventions

- Go monorepo. `mock-price-server/` and `stocky/` are separate Go modules, each with its own `go.mod`, tied together by `go.work`. `stocky` must never import from `mock-price-server`; they talk only over HTTP.
- gin for routing, `github.com/sirupsen/logrus` for all logging (no `fmt.Println`, no gin default logger, and no plain `gin.Recovery()`: use the logrus-backed `gin.CustomRecoveryWithWriter` already in each router).
- Money and quantities: `shopspring/decimal` in Go, `NUMERIC` in Postgres, strings in JSON. Never `float64`, including DB reads (pgx-shopspring-decimal is registered in `db.NewPool`).
- Prices and INR amounts use 4 decimal places; quantities use 6. Format with `money.INR` / `money.Qty`.
- All timestamps in API responses and logs are IST (`Asia/Kolkata`), RFC3339 with a `+05:30` offset. "Today" in the main app means the IST calendar day. Import `_ "time/tzdata"` and never rely on the machine's TZ.
- Do not use `time.Truncate(time.Hour)` or similar for IST boundaries: it rounds on UTC boundaries, which fall at :30 in IST. Use the `timeutil` helpers.
- Ledger rows are never updated or deleted. Corrections are new rows. DB triggers enforce this on `ledger_transactions`, `ledger_entries` and `stock_prices` (TRUNCATE is allowed).
- Every ledger transaction must balance per asset; always write through `ledger.Post` inside `db.WithTx`.
- Do not use em dashes in docs or comments.
- Before finishing a change, run `go vet ./...` and `go test ./...` inside the module you touched.

# stocky specifics

- Layout: handlers in `internal/api` only parse, call a service, and map errors via `writeError`. Logic lives in `rewards`, `portfolio`, `corporate`, `pricing`, `ledger`.
- Config: `stocky` loads only `stocky/.env` (gitignored; template in `stocky/.env.example`). It must not load the root `.env`, which is the mock server's config and shares variable names.
- Integration tests: set `TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/assignment_test?sslmode=disable`. Without it they skip. Each test package's `TestMain` calls `testdb.Main(m)`; tests call `testdb.Pool(t)`, which truncates tables. `testdb` holds an advisory lock per test binary so parallel packages do not collide.
- Idempotency of `POST /reward` is enforced by `INSERT ... ON CONFLICT (idempotency_key) DO NOTHING` inside the transaction, not by a Go-side check. The request hash uses the `rewarded_at` the client sent, not the defaulted "now".
- Corporate actions adjust balances as they stand when recorded. When asking "did an action happen after this reward", compare `created_at`, not `effective_at`.
- Postgres runs locally (user and password `postgres`, databases `assignment` and `assignment_test`). There is no Docker setup in this repo.
