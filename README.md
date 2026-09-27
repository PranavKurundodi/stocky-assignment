# Stocky Assignment

Stocky rewards users with shares of Indian stocks. This repo contains two
separate Go services that talk only over HTTP:

| Service | What it is | Docs |
|---|---|---|
| [`stocky/`](stocky/) | The assignment: reward API, double-entry ledger (share units, INR cash, fees), hourly price fetch, portfolio and history endpoints, splits, mergers, delistings and reversals. PostgreSQL database `assignment`. | [stocky/README.md](stocky/README.md) |
| [`mock-price-server/`](mock-price-server/) | A stand-in for an external price vendor. Serves random-walk NSE prices and lets you simulate outages, price jumps, listings and delistings. No database. | [mock-price-server/README.md](mock-price-server/README.md) |

Both are written in Go with gin and logrus, use `shopspring/decimal` for money
(never `float64`), and report every timestamp in IST (`+05:30`).

## Quick start

```sh
# Postgres: databases "assignment" and "assignment_test", user and password postgres
sudo -u postgres psql -c "ALTER USER postgres PASSWORD 'postgres';" \
  -c "CREATE DATABASE assignment;" -c "CREATE DATABASE assignment_test;"

(cd mock-price-server && go run .)             # terminal 1: prices on :8081
(cd stocky && go run ./cmd/server)             # terminal 2: API on :8080
(cd stocky && go run ./cmd/seed --reset)       # optional demo data
```

Then import [`postman/Stocky.postman_collection.json`](postman/Stocky.postman_collection.json)
into Postman: folder "Stocky" has every endpoint and "Stocky scenarios";
folder "Mock price server" controls the vendor.

## Layout

```
go.work                  workspace covering both modules
.env                     mock price server config (committed on purpose)
stocky/                  main app (own go.mod); config template in stocky/.env.example
mock-price-server/       price vendor stand-in (own go.mod)
postman/                 Postman collection for both services
```
