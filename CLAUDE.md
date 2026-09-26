# Repo conventions

- Go monorepo. `mock-price-server/` and `stocky/` are separate Go modules, each with its own `go.mod`. `stocky` must never import from `mock-price-server`; they talk only over HTTP.
- gin for routing, `github.com/sirupsen/logrus` for all logging (no `fmt.Println`, no gin default logger).
- Money and quantities: `shopspring/decimal` in Go, `NUMERIC` in Postgres, strings in JSON. Never `float64`.
- Prices use 4 decimal places; quantities use 6.
- All timestamps in API responses and logs are IST (`Asia/Kolkata`), RFC3339 with a `+05:30` offset. "Today" in the main app means the IST calendar day. Import `_ "time/tzdata"` and never rely on the machine's TZ.
- Ledger rows are never updated or deleted. Corrections are new rows.
- Do not use em dashes in docs or comments.
- Before finishing a change, run `go vet ./...` and `go test ./...` inside the module you touched.
