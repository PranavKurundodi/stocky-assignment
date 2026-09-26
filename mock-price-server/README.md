# Mock price server

A stand-in for an external stock price vendor. It keeps current prices for a
few NSE stocks in memory, moves them randomly on a timer and serves them over
HTTP. Admin endpoints let you simulate the things real vendors do to you:
outages, sudden price jumps, listings and delistings.

## Why a separate service

The Stocky app has to fetch prices from a vendor every hour and cope when that
vendor misbehaves. Running the vendor as its own process, in its own Go module,
keeps the boundary honest:

- Stocky talks to it only over HTTP, exactly as it would talk to a real vendor.
  Stocky never imports this module.
- Failures are real network failures (503s, refused connections), not mocked
  function calls, so Stocky's retries and stale-price handling are exercised
  for real.
- Swapping in a real vendor later means changing a base URL, not code.

## Running it

```sh
cd mock-price-server
go run .
```

It listens on `http://localhost:8081` by default. Configuration is read from
the repo root `.env` (see [Config](#config)). Stop it with Ctrl+C; it shuts
down gracefully.

Tests:

```sh
go vet ./...
go test ./...
```

## Timestamps are IST

Every timestamp in every response (`as_of`) and every log line is in IST,
RFC3339 with an explicit offset, for example `2026-09-26T10:31:00+05:30`.
Never UTC and never `Z`. The zone database is embedded in the binary
(`time/tzdata`), so this holds regardless of the machine's `TZ` setting.

## How prices move

Starting prices: RELIANCE 1200, TCS 2000, INFY 1000, ICICIBANK 1400,
HDFCBANK 750.

On every tick (default once a minute) each stock independently moves by exactly
+1% or -1%, rounded to 4 decimal places, and its `as_of` is updated. The market
keeps ticking during a simulated outage. Restarting the process resets
everything to the starting prices.

Prices are always JSON strings with exactly 4 decimal places.

## Endpoints

All errors use one shape: `{"error": "message", ...optional extra fields}`.

### Price endpoints

These are what Stocky calls. All three are affected by the outage
simulation.

#### `GET /health`

```
200 {"status":"ok"}
503 {"status":"down"}            while in outage mode (with Retry-After: 30)
```

#### `GET /prices`

All quotes, sorted by symbol.

```json
200
{
  "prices": [
    {"symbol": "HDFCBANK",  "price": "750.0000",  "as_of": "2026-09-26T10:31:00+05:30"},
    {"symbol": "ICICIBANK", "price": "1400.0000", "as_of": "2026-09-26T10:31:00+05:30"},
    {"symbol": "INFY",      "price": "1000.0000", "as_of": "2026-09-26T10:31:00+05:30"},
    {"symbol": "RELIANCE",  "price": "1200.0000", "as_of": "2026-09-26T10:31:00+05:30"},
    {"symbol": "TCS",       "price": "2000.0000", "as_of": "2026-09-26T10:31:00+05:30"}
  ]
}
```

#### `GET /prices/:symbol`

The symbol is case and whitespace insensitive.

```json
200 {"symbol":"RELIANCE","price":"1200.0000","as_of":"2026-09-26T10:31:00+05:30"}
404 {"error":"unknown stock symbol","symbol":"FAKECORP"}
```

While in outage mode, both price routes return:

```json
503 {"error":"price service unavailable"}      (with Retry-After: 30)
```

### Admin endpoints

Admin endpoints keep working during an outage, so you can always recover. If `ADMIN_TOKEN` is set, every admin request needs the header
`X-Admin-Token: <token>`, otherwise it gets
`401 {"error":"missing or invalid admin token"}`.

Price rules for request bodies: the price must be a JSON **string** (a JSON
number such as `1200` is rejected), greater than zero, with at most 4 decimal
places. Invalid prices are rejected, never rounded.

Symbol rules: trimmed and uppercased, then must match `^[A-Z0-9&-]{1,20}$`, so
real NSE symbols such as `M&M` and `BAJAJ-AUTO` work.

#### `PUT /admin/prices/:symbol`

Override a price. Ticking continues from the new price.

```json
request  {"price":"1000.0000"}
200      {"symbol":"RELIANCE","price":"1000.0000","as_of":"2026-09-26T10:35:12+05:30"}
400      {"error":"price must have at most 4 decimal places"}
404      {"error":"unknown stock symbol","symbol":"FAKECORP"}
```

#### `POST /admin/stocks`

List a new stock. It ticks like the others from the next tick onward.

```json
request  {"symbol":"WIPRO","price":"500.0000"}
201      {"symbol":"WIPRO","price":"500.0000","as_of":"2026-09-26T10:35:12+05:30"}
400      {"error":"symbol must be 1-20 characters of A-Z, 0-9, & or -"}
409      {"error":"stock symbol already exists","symbol":"WIPRO"}
```

#### `DELETE /admin/stocks/:symbol`

Delist a stock. It simply stops being quoted.

```
204      (no body)
404      {"error":"unknown stock symbol","symbol":"FAKECORP"}
```

#### `POST /admin/fail`

Start outage mode. Idempotent.

```json
200 {"down":true}
```

#### `POST /admin/recover`

End outage mode. Idempotent. Everything changed during the outage (ticks,
overrides, added and removed stocks) is kept.

```json
200 {"down":false}
```

#### `GET /admin/state`

```json
200
{
  "down": false,
  "tick_interval": "1m0s",
  "prices": [ {"symbol":"HDFCBANK","price":"750.0000","as_of":"2026-09-26T10:31:00+05:30"}, "..." ]
}
```

## Config

Read from environment variables. A `.env` file (in the current directory or
the repo root) is loaded if present; real environment variables win over it,
and the defaults below apply to anything unset. Invalid values stop the server
at startup with a clear error.

| Variable | Default | Meaning |
|---|---|---|
| `PRICE_SERVER_PORT` | `8081` | HTTP port |
| `PRICE_TICK_INTERVAL` | `1m` | How often prices move (Go duration: `30s`, `1m`, `2m30s`) |
| `PRICE_SERVER_START_DOWN` | `false` | Start in outage mode |
| `LOG_LEVEL` | `debug` | `trace`, `debug`, `info`, `warn`, `error` (`debug` shows every tick) |
| `GIN_MODE` | `debug` | `debug`, `release` or `test` |
| `ADMIN_TOKEN` | empty | If set, `/admin/*` requires `X-Admin-Token` |

## Simulation scenarios

The Postman collection (`postman/Stocky.postman_collection.json`, folder
"Mock price server" > "Scenarios") has these ready to run in order.

| Scenario | What to do on the mock | What it simulates |
|---|---|---|
| Outage | `POST /admin/fail`, later `POST /admin/recover` | Vendor down: price calls get 503 with `Retry-After`. Stocky should keep the last known prices, mark them stale and retry. |
| Stock split | `PUT /admin/prices/RELIANCE` with half the current price | A 2:1 split: price halves overnight. Stocky must double the units held, or valuations drop by half. |
| Delisting | `DELETE /admin/stocks/HDFCBANK` | Stock no longer quoted: 404 for that symbol. Stocky should keep valuing it at the last known price and flag it. |
| Merger | `DELETE` the acquired stock, `POST /admin/stocks` for the acquirer if missing | Target stops trading, holders get acquirer shares at a ratio. Stocky converts holdings. |
| New listing | `POST /admin/stocks {"symbol":"WIPRO","price":"500.0000"}` | A new stock becomes available to reward. |

### Stopping the process is a second kind of downtime

Killing the server also simulates the vendor being down, but it behaves
differently from `/admin/fail`:

| | `POST /admin/fail` | Stopping the process |
|---|---|---|
| What callers see | HTTP `503` with `Retry-After: 30` | `connection refused` (no HTTP response at all) |
| State afterwards | Preserved: prices, overrides and listings all survive recovery | Lost: a restart resets everything to base prices |
| Recover with | `POST /admin/recover` | Start the process again |

Stocky should handle both: one is an HTTP error status, the other is a
transport error before any status exists.

## Layout

```
main.go                        config, logging, wiring, graceful shutdown
internal/market/               in-memory market: quotes, ticks, add/remove/override
internal/api/router.go         routes and middleware order
internal/api/prices.go         /health, /prices, /prices/:symbol
internal/api/admin.go          /admin/*
internal/api/validate.go       price and symbol validation
internal/middleware/           request logger, outage, admin token
```
