# Stocky

Stocky rewards users with shares of Indian stocks (RELIANCE, TCS, INFY, ...)
for actions like onboarding, referrals and trading milestones. The user always
receives the full number of shares; Stocky buys them on the exchange and pays
the brokerage, taxes and fees itself. Those costs are invisible to the user but
recorded internally.

This service:

- records reward events, idempotently, via `POST /reward`
- keeps a **double-entry ledger** of share units, INR cash outflow and every fee
- fetches prices hourly from a price vendor (here the
  [mock price server](../mock-price-server/README.md)) over HTTP
- serves today's rewards, current portfolio value, stats and daily history
- handles reversals, stock splits, mergers and delistings

Go, gin, logrus, PostgreSQL (pgx), shopspring/decimal. Money never touches
`float64`.

## Contents

- [Running it](#running-it)
- [Testing with Postman](#testing-with-postman)
- [Configuration](#configuration)
- [API](#api)
- [Database schema](#database-schema)
- [The ledger](#the-ledger)
- [Edge cases](#edge-cases)
- [Assumptions](#assumptions)
- [Scaling](#scaling)
- [Code layout](#code-layout)
- [Tests](#tests)

## Running it

Everything below runs from the repo root unless it says `cd`.

**1. Postgres.** A local PostgreSQL (developed on 17) with user `postgres`,
password `postgres`, and two databases: `assignment` for the app and
`assignment_test` for the integration tests. One-time setup:

```sh
sudo -u postgres psql -c "ALTER USER postgres PASSWORD 'postgres';" \
  -c "CREATE DATABASE assignment;" -c "CREATE DATABASE assignment_test;"
```

**2. The mock price server** (in its own terminal):

```sh
cd mock-price-server && go run .
```

**3. Stocky** (in another terminal). Migrations run automatically at startup,
and the first price fetch happens immediately:

```sh
cd stocky && go run ./cmd/server
```

**4. Demo data** (optional): 3 users, 30 days of hourly prices and 16 rewards,
created through the same code path as `POST /reward`:

```sh
cd stocky && go run ./cmd/seed --reset
```

`--reset` empties users, prices, rewards and the ledger first, and puts `stocks`
back to the five base stocks (all ACTIVE, extra symbols removed). Without it the
seed is safe to rerun on the same day (every reward is replayed, every price
skipped); on a later day use `--reset`, because the reward keys include the date.

Run Stocky and the seed from `stocky/`: they load `stocky/.env` if present and
deliberately never the repo root `.env`, which belongs to the mock server.

## Testing with Postman

Import [`postman/Stocky.postman_collection.json`](../postman/Stocky.postman_collection.json).
It has two folders: **Mock price server** (the vendor and its admin controls)
and **Stocky** (every endpoint below, plus **Stocky scenarios**). The
collection variables `stockyBaseUrl` and `priceBaseUrl` default to
`http://localhost:8080` and `http://localhost:8081`.

**Start from a clean slate** before running the scenarios, because the split,
merger and delisting scenarios change data on both services:

```sh
# terminal 1: restart the mock (its state is in memory, so a restart resets it)
cd mock-price-server && go run .

# terminal 2: reset Stocky's data, then start it
cd stocky && go run ./cmd/seed --reset
PRICE_STALE_AFTER=5m PRICE_FETCH_INTERVAL=1m go run ./cmd/server
```

The two overrides are for demos only: prices are fetched every minute and go
stale after 5 minutes, so stale-price behaviour shows up quickly. Always change
both together; with a short stale window and the default hourly fetch, every
reward is refused as stale a few minutes after startup.

**Stocky scenarios**, each run top to bottom (each request checks its own
status and results; see its Test Results tab):

| Scenario | What it shows |
|---|---|
| Reward then replay | Same `Idempotency-Key` and body: 201, then 200 with the same reward |
| Key reuse with a different body | Same key, different quantity: 409 |
| Reverse twice | Reversal is idempotent: 200, then 200 with nothing written |
| Outage demo | Mock fails; refresh gives 503 and Stocky keeps its last prices; after `PRICE_STALE_AFTER` they are flagged `is_stale`; mock recovers |
| Split | RELIANCE 1:2 in Stocky, the mock halves the price, Stocky refreshes: holding doubled, value about the same |
| Merger | ICICIBANK into HDFCBANK 2:1; the mock stops quoting ICICIBANK; the holding is converted |
| Delisting | HDFCBANK delisted; the mock stops quoting it and Stocky does not report it missing; new rewards on it get 422; holdings stay, valued at the last price |
| Ledger verify | Every ledger transaction balances |

To run them all at once, right-click **Stocky scenarios** and choose
**Run folder**.

## Configuration

Environment variables, all optional. See [`.env.example`](.env.example).
Invalid values stop the server at startup with a clear error.

| Variable | Default | Meaning |
|---|---|---|
| `STOCKY_PORT` | `8080` | HTTP port |
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/assignment?sslmode=disable` | Postgres |
| `PRICE_SERVER_URL` | `http://localhost:8081` | Price vendor base URL |
| `PRICE_FETCH_INTERVAL` | `1h` | Background fetch interval |
| `PRICE_STALE_AFTER` | `2h` | Age after which a price is stale |
| `PRICE_CLIENT_TIMEOUT` | `5s` | Timeout per vendor attempt |
| `FEE_BROKERAGE_FLAT` | `20` | Flat brokerage per purchase, INR |
| `LOG_LEVEL` | `info` | logrus level |
| `GIN_MODE` | `debug` | gin mode |

## API

Conventions for every endpoint:

- Decimals are **strings** with fixed places: prices and INR amounts 4
  (`"2000.0000"`), share quantities 6 (`"1.500000"`). Request bodies must send
  them as strings too; a JSON number is rejected with 400.
- Timestamps are RFC3339 in **IST**: `2026-09-26T12:00:00+05:30`.
  "Today" is the IST calendar day, `[00:00 IST, next 00:00 IST)`.
- Errors are `{"error": "message", ...extra}`, e.g.
  `{"error":"unknown user","user_id":"ghost"}`.
- Any user endpoint with an unknown user returns 404. Symbols are trimmed and
  uppercased.

### `GET /health`

Pings the database.

```
200 {"status":"ok"}
503 {"status":"db unavailable"}
```

### `POST /users`

```json
request  {"id":"user_4","name":"Ravi Kumar"}
201      {"id":"user_4","name":"Ravi Kumar"}
409      {"error":"user already exists","user_id":"user_4"}
400      {"error":"id must be 1-64 letters, digits, _ or -, and name must not be empty"}
```

### `POST /reward`

Header `Idempotency-Key` is required. `rewarded_at` is optional (defaults to
now) and may be in the past (backdated rewards are priced at that time).

```http
POST /reward
Idempotency-Key: 6f1c1f1e-8a1d-4b77-9a55-0c4f2f0f7a10

{"user_id":"user_1","symbol":"TCS","quantity":"1","reason":"REFERRAL",
 "rewarded_at":"2026-09-26T12:00:00+05:30"}
```

```json
201
{
  "reward_id": "0b6f4a3e-2f6b-4d5f-9c0a-6d2d3c1b9e11",
  "user_id": "user_1",
  "symbol": "TCS",
  "quantity": "1.000000",
  "reason": "REFERRAL",
  "rewarded_at": "2026-09-26T12:00:00+05:30",
  "price_used": "2000.0000",
  "price_as_of": "2026-09-26T11:50:00+05:30",
  "cost_inr": "2000.0000",
  "fees": {
    "brokerage": "20.0000", "stt": "2.0000", "exchange": "0.0594",
    "sebi": "0.0020", "stamp_duty": "0.3000", "gst": "3.6111", "total": "25.9725"
  },
  "status": "ACTIVE",
  "reversed_at": null
}
```

| Case | Status |
|---|---|
| Created | 201 |
| Same key, same body (a retry) | 200 with the original reward, nothing written |
| Same key, different body | 409 |
| Missing key, malformed JSON, quantity not a string, quantity <= 0 or > 6 dp, bad reason, `rewarded_at` > 1 minute in the future | 400 |
| Unknown user or symbol | 404 |
| Symbol delisted | 422 |
| No price at or before `rewarded_at` | 422 `no price available at rewarded_at` |
| Price at `rewarded_at` older than `PRICE_STALE_AFTER` | 503 `price data stale` |

`reason` is one of `ONBOARDING`, `REFERRAL`, `MILESTONE`, `OTHER`.

### `POST /reward/:id/reverse`

Takes the shares back (see [reversals](#reversals)). Idempotent: reversing
again returns 200 with the same reward and writes nothing.

```
200  the reward, now with "status":"REVERSED" and "reversed_at" set
404  unknown or malformed id
422  a split or merger of the stock was recorded after the reward
```

### `GET /today-stocks/:userId`

Rewards with `rewarded_at` in today's IST window, newest first. Reversed
rewards are listed with their status.

```json
{
  "user_id": "user_1",
  "date": "2026-09-27",
  "rewards": [
    {"reward_id":"cc5ef4a5-...","symbol":"TCS","quantity":"1.000000",
     "reason":"MILESTONE","rewarded_at":"2026-09-27T00:30:00+05:30","status":"ACTIVE"}
  ]
}
```

### `GET /stats/:userId`

```json
{
  "user_id": "user_1",
  "today_shares_by_symbol": {"TCS": "1.000000"},
  "current_portfolio_inr": "11410.5573",
  "prices_as_of": "2026-09-27T03:51:39+05:30",
  "is_stale": false
}
```

`today_shares_by_symbol` counts ACTIVE rewards given today. `prices_as_of` is
the **oldest** price used in the valuation (null if nothing could be valued),
and `is_stale` is true if any price used is older than `PRICE_STALE_AFTER`.
`missing_prices` appears when a held symbol has no price at all.

### `GET /portfolio/:userId`

```json
{
  "user_id": "user_1",
  "holdings": [
    {"symbol":"HDFCBANK","quantity":"3.000000","price":"819.8541",
     "price_as_of":"2026-09-27T03:51:39+05:30","value_inr":"2459.5623",
     "is_stale":false,"delisted":false},
    {"symbol":"WIPRO","quantity":"1.000000","price":null,
     "price_as_of":null,"value_inr":null,"is_stale":false,"delisted":false}
  ],
  "total_value_inr": "2459.5623",
  "missing_prices": ["WIPRO"]
}
```

Zero holdings are omitted. Delisted stocks are valued at their last known
price with `delisted: true`. A symbol with no price at all has null price
fields, is excluded from the total and is listed in `missing_prices`.

### `GET /historical-inr/:userId`

The end-of-day INR value of everything the user held, for each IST date from
their first ledger entry up to and including **yesterday** (today has not
closed).

```json
{
  "user_id": "user_1",
  "history": [
    {"date":"2026-08-30","inr_value":"2274.8574"},
    {"date":"2026-08-31","inr_value":"2412.6348"},
    {"date":"2026-09-25","inr_value":null,"missing_prices":["WIPRO"]}
  ]
}
```

For each day: holdings from ledger entries with `effective_at` before the next
IST midnight, times each symbol's closing price (its latest price before the
next IST midnight). If any held symbol has no price yet that day, the value is
null and `missing_prices` lists them rather than reporting a misleadingly low
total. A user with no rewards gets `"history": []`.

### `POST /admin/prices/refresh`

Runs one price fetch now.

```json
200 {"fetched_at":"2026-09-27T03:32:17+05:30","inserted":1,"skipped":4,
     "new_symbols":["WIPRO"],"missing_symbols":["HDFCBANK"]}
503 {"error":"price service unavailable"}
```

### `POST /admin/corporate-actions`

```json
request  {"type":"SPLIT","symbol":"RELIANCE","ratio_from":"1","ratio_to":"2"}
request  {"type":"MERGER","symbol":"ICICIBANK","new_symbol":"HDFCBANK","ratio_from":"2","ratio_to":"1"}
request  {"type":"DELISTING","symbol":"HDFCBANK"}
         ("effective_at" is optional on all three and defaults to now)

201 {"id":"...","type":"SPLIT","symbol":"RELIANCE","new_symbol":null,
     "ratio_from":"1.000000","ratio_to":"2.000000",
     "effective_at":"2026-09-27T10:00:00+05:30","holders_affected":3}
400  invalid type, ratios not positive decimal strings, MERGER without new_symbol, ...
404  unknown symbol or new_symbol
422  symbol or new_symbol not ACTIVE
```

`ratio_from:ratio_to` reads as "from old shares to new shares": a 1:2 split
doubles holdings, a 2:1 reverse split halves them, and a 3:1 merger turns 3
shares of `symbol` into 1 share of `new_symbol`.

### `GET /admin/ledger/verify`

Re-checks, in SQL, that every ledger transaction balances per asset.

```json
200 {"checked":16,"unbalanced":[]}
```

## Database schema

Migrations are embedded in the binary and applied at startup
(`migrations/001_init.up.sql`).

```
users ─┬─< reward_events >── stocks ──< stock_prices
       │        │               │ └──< corporate_actions (symbol, new_symbol)
       │        │               │              │
       │        └──< ledger_transactions >─────┘   (reward_id or corporate_action_id)
       │                   │
       └────────< ledger_entries        (user_id only on USER_STOCK lines)
```

| Table | Purpose | Key points |
|---|---|---|
| `users` | Reward recipients | `id` is a URL-safe string |
| `stocks` | Known symbols | `status` ACTIVE or DELISTED; five base stocks seeded by the migration |
| `stock_prices` | Every quote fetched | `NUMERIC(18,4)`, `as_of` (vendor time) and `fetched_at`; `UNIQUE (symbol, as_of)`; index `(symbol, as_of DESC)`; append only |
| `reward_events` | One row per reward | `idempotency_key UNIQUE`, `request_hash`, `quantity NUMERIC(18,6)`, the `price_used` and its `price_as_of`, `status` ACTIVE or REVERSED |
| `corporate_actions` | Splits, mergers, delistings | Ratios `NUMERIC(18,6)`; CHECKs that splits and mergers have ratios and only mergers have `new_symbol` |
| `ledger_transactions` | One balanced posting | `type`, optional `reward_id` or `corporate_action_id`, `effective_at` (economic time) |
| `ledger_entries` | Debit and credit lines | `account`, `user_id`, `asset` (`INR` or a symbol), `direction`, and exactly one of `quantity NUMERIC(18,6)` or `inr_amount NUMERIC(18,4)` |

Constraints worth pointing out:

- An `INR` line has `inr_amount` and no `quantity`; a stock line the reverse.
  Whichever amount is set must be `> 0` (the sign is the direction).
- `user_id` is set exactly on `USER_STOCK` lines.
- `ledger_transactions`, `ledger_entries` and `stock_prices` have triggers that
  reject every `UPDATE` and `DELETE`. History cannot be rewritten; corrections
  are new rows. (`TRUNCATE` is allowed, for `seed --reset` and tests.)
- There is **no holdings table**. Holdings are always derived from the ledger.

## The ledger

Every movement is a transaction of debit and credit lines. Before anything is
written, `ledger.Post` drops zero-amount lines, rejects amounts with more
decimals than their column, and requires **debits = credits for each asset
separately**, so INR can never offset shares and TCS can never offset INFY.
The reward row and its ledger posting are written in one DB transaction.

### Accounts

| Account | Asset | Meaning |
|---|---|---|
| `COMPANY_CASH` | INR | Cash Stocky spends (credit = money out) |
| `REWARD_EXPENSE` | INR | Cost of shares given away |
| `FEE_BROKERAGE`, `FEE_STT`, `FEE_EXCHANGE`, `FEE_SEBI`, `FEE_STAMP_DUTY`, `FEE_GST` | INR | Each purchase charge, separately |
| `STOCK_INVENTORY_VALUE` | INR | Value of shares Stocky took back |
| `USER_STOCK` (+ user_id) | units | A user's shares |
| `COMPANY_STOCK` | units | Shares Stocky holds itself |
| `MARKET` | units | Counterparty for shares bought on the exchange |
| `CORPORATE_ACTION` | units | Counterparty for shares created or removed by splits and mergers |

A balance is `sum(DEBIT) - sum(CREDIT)`. A user's holding of a symbol is the
balance of their `USER_STOCK` account in that asset, filtered by
`effective_at` when valuing a past date.

### Worked example: 1 TCS at 2000.0000

`cost = 1 x 2000.0000 = 2000.0000`. Fees on 2000.0000 with the default Rs 20 brokerage:

| Component | Rate | Amount |
|---|---|---|
| Brokerage | flat Rs 20 | 20.0000 |
| STT | 0.1% | 2.0000 |
| Exchange charge | 0.00297% | 0.0594 |
| SEBI fee | 0.0001% | 0.0020 |
| Stamp duty | 0.015% | 0.3000 |
| GST | 18% of (brokerage + exchange + SEBI) = 18% of 20.0614 | 3.6111 |
| **Total** | | **25.9725** |

The REWARD transaction (`effective_at` = `rewarded_at`):

| Account | Asset | Debit | Credit |
|---|---|---|---|
| REWARD_EXPENSE | INR | 2000.0000 | |
| FEE_BROKERAGE | INR | 20.0000 | |
| FEE_STT | INR | 2.0000 | |
| FEE_EXCHANGE | INR | 0.0594 | |
| FEE_SEBI | INR | 0.0020 | |
| FEE_STAMP_DUTY | INR | 0.3000 | |
| FEE_GST | INR | 3.6111 | |
| COMPANY_CASH | INR | | 2025.9725 |
| USER_STOCK (user_1) | TCS | 1.000000 | |
| MARKET | TCS | | 1.000000 |

INR: 2025.9725 = 2025.9725. TCS: 1 = 1. Any zero-amount fee line (for example
brokerage with `FEE_BROKERAGE_FLAT=0`) is dropped, not stored.

### Reversal of that reward

Stocky keeps the shares and gets no money back; the fees are sunk. So the
reversal is **not** a mirror of the reward:

| Account | Asset | Debit | Credit |
|---|---|---|---|
| COMPANY_STOCK | TCS | 1.000000 | |
| USER_STOCK (user_1) | TCS | | 1.000000 |
| STOCK_INVENTORY_VALUE | INR | 2000.0000 | |
| REWARD_EXPENSE | INR | | 2000.0000 |

Afterwards `COMPANY_CASH` and every fee account are unchanged.

### Split 1:2 and merger 3:1

A 1:2 split of TCS, for every holder with holding `h` (each user and
`COMPANY_STOCK`): `extra = h x 2/1 - h`, rounded to 6 dp.
`Dr holder TCS extra`, `Cr CORPORATE_ACTION TCS extra`.

A 3:1 merger of INFY into TCS, for every holder of `h` INFY:
`Dr CORPORATE_ACTION INFY h`, `Cr holder INFY h`,
`Dr holder TCS h/3`, `Cr CORPORATE_ACTION TCS h/3`; then INFY is DELISTED.
A holder of 7.5 INFY ends with 2.5 TCS.

## Edge cases

### Duplicate reward events and replay attacks

- `POST /reward` requires an `Idempotency-Key`. The body is normalized
  (symbol and reason uppercased, quantity to 6 dp, `rewarded_at` in UTC) and
  hashed with SHA-256, so `"2"` and `"2.000000"` are the same request.
- The guarantee comes from the database, not from a check in Go: the reward is
  inserted with `ON CONFLICT (idempotency_key) DO NOTHING` inside the
  transaction. If no row comes back, the key exists; the transaction rolls back
  and the stored row's hash is compared (same: 200 with the original reward;
  different: 409). A concurrent request with the same key blocks on the unique
  index until the first commits, so exactly one wins. A test fires 10
  simultaneous identical requests and checks one reward and one ledger posting.
- If `rewarded_at` is omitted, the hash uses the omitted value, not the
  resolved "now", so a retry of the same body is a replay rather than a 409.
- A retry succeeds even if the stock was delisted or its price went stale in
  between, because a known key is answered from the stored reward first.
- Reversals are idempotent too (`SELECT ... FOR UPDATE` on the reward row).

### Stock splits, mergers and delisting

- Splits and mergers are recorded in `corporate_actions` and posted to the
  ledger against the `CORPORATE_ACTION` account, adjusting every holder (users
  and `COMPANY_STOCK`) in one transaction. The stock row is locked `FOR UPDATE`
  meanwhile, which blocks new rewards on it (they take `FOR SHARE`).
- The vendor, not Stocky, changes the price after a split. On the mock, halve
  the price with `PUT /admin/prices/:symbol` and refresh.
- Delisting only changes the stock's status: holders keep their shares, the
  portfolio values them at the last known price with `delisted: true`, and new
  rewards on it return 422.
- A symbol missing from the price feed is **never** auto-delisted; it is only
  logged. Delisting is a deliberate action.
- New symbols appearing in the feed are registered as ACTIVE automatically.

### Rounding errors in INR valuation

- Money is `shopspring/decimal` in Go, `NUMERIC` in Postgres and strings in
  JSON. NUMERIC scans straight into decimals (pgx-shopspring-decimal), so
  there is no float anywhere in the money path.
- Quantities with more than 6 decimals are rejected, never rounded.
- `cost = quantity x price`, rounded **once**, half-up, to 4 dp.
- Each fee component is rounded to 4 dp; the fee total is the sum of the
  rounded components, so the ledger lines add up exactly to the cash credited.
- Valuations multiply and sum at full precision and round once at the end. Each
  holding's displayed `value_inr` is rounded separately, so rows can differ
  from `total_value_inr` by a few ten-thousandths; the total is the accurate one.
- `ledger.Post` rejects any amount with more decimals than its column, so
  Postgres can never silently round a line and unbalance a transaction.

### Price API downtime or stale data

- The client makes up to 3 attempts per fetch (waiting 1s, then 2s), with a
  timeout per attempt. Non-2xx responses, timeouts and refused connections are
  all treated as "unavailable". Cancellation (shutdown) interrupts the backoff.
- A failed fetch writes **nothing**: no zero prices, no nulls. The last good
  prices stay in place. Invalid individual quotes are skipped and logged.
- Every price keeps the vendor's `as_of`. Reads compare it with now: the
  portfolio and stats report `is_stale` and the oldest `prices_as_of`.
- `POST /reward` compares the price's `as_of` with `rewarded_at` and returns
  503 if it is older than `PRICE_STALE_AFTER`, rather than booking a reward
  at an unknown price.
- A held symbol with no price at all is reported (`null` plus `missing_prices`)
  instead of being counted as zero.

### Adjustments and refunds (reversals)

- `POST /reward/:id/reverse` moves the shares from the user to
  `COMPANY_STOCK` and moves the original cost from `REWARD_EXPENSE` to
  `STOCK_INVENTORY_VALUE`. Cash and fees are untouched.
- Nothing is deleted or updated in the ledger; the reward row gets
  `status = REVERSED` and `reversed_at`, and `/today-stocks` still lists it.
- `/historical-inr` stays correct: days before the reversal still include the
  shares, because the reversal's `effective_at` is when it happened.

## Assumptions

- **historical-inr** is the end-of-day value of everything the user held on
  each past day (holdings at close times closing price), not the value of that
  day's rewards alone. Today is excluded because the day has not closed.
- **Stale prices are rejected for new rewards** (503) instead of booking at an
  old price. Reads still answer, flagged `is_stale`.
- **No auto-delisting**: a stock missing from the feed stays ACTIVE until a
  DELISTING corporate action.
- **A reversal does not refund cash or fees**: Stocky already bought the
  shares and keeps them in inventory.
- **A reward cannot be reversed after a split or merger** of its stock was
  recorded (422); the original quantity no longer matches what the user holds,
  so that needs a manual adjustment.
- **Splits and mergers adjust current balances.** `effective_at` dates the
  ledger transaction (so history shows the change on that day), but the
  holders are read at the time the action is recorded. Stored prices are never
  adjusted; the vendor's prices change when the vendor changes them.
- **Brokerage** defaults to a flat Rs 20 per purchase (`FEE_BROKERAGE_FLAT`).
  Brokers differ; some charge 0 for delivery.
- **Fee rates** are modelled on NSE equity delivery buy charges (STT 0.1%,
  exchange 0.00297%, SEBI 0.0001%, stamp duty 0.015%, GST 18% on brokerage +
  exchange + SEBI). Rates change; verify against a current broker charge sheet.
- **Fractional shares** are allowed, as the brief requires (`NUMERIC(18,6)`).
  Indian exchanges trade whole shares, so in practice a broker would buy whole
  shares into its own inventory and allocate fractions on its books. The ledger
  records the user's fractional holding directly against `MARKET`; a fuller
  model would buy whole shares into `COMPANY_STOCK` and allocate from there.
- `rewarded_at` may be up to 1 minute in the future to absorb clock skew.
- `/admin/*` endpoints have no authentication in this assignment.

## Scaling

- **Stateless API.** All state is in Postgres, so the API scales horizontally
  behind a load balancer. Idempotency does not depend on which instance
  receives a retry, because it is enforced by a unique constraint.
- **One price job, many instances.** Today every instance would run the hourly
  fetch. Duplicates are harmless (`ON CONFLICT (symbol, as_of)`), but wasteful.
  The fix is to wrap each run in `pg_try_advisory_lock`, so only the instance
  that gets the lock fetches (migrations already work this way).
- **Holdings and daily snapshots.** Holdings are summed from the ledger on every
  read, and `historical-inr` does one holdings query per day. At scale, add a
  `holdings` table maintained in the same transaction as each posting, and a
  nightly `daily_valuations (user_id, date, inr_value)` snapshot so history is
  one indexed range read. The ledger stays the source of truth; both tables can
  be rebuilt from it.
- **Partitioning.** `ledger_entries` and `stock_prices` are append only and grow
  forever; partition both by month (range on `effective_at` / `as_of`). Old
  partitions become read only and cheap to archive.
- **Connection pooling.** pgxpool per instance, sized to Postgres limits; put
  PgBouncer in front when instance count grows.
- **Reads.** Portfolio and stats reads could go to a replica; `/reward` must use
  the primary.

## Code layout

```
cmd/server/          main: config, logging, pool, migrations, price job, HTTP, shutdown
cmd/seed/            demo data generator (uses the rewards service)
migrations/          embedded SQL migrations
internal/config/     env vars with defaults, fail-fast validation
internal/timeutil/   IST zone, day windows, RFC3339 formatting, IST log formatter
internal/db/         pgxpool with decimal support, WithTx, migrations, ResetData
internal/pricing/    vendor HTTP client with retries, hourly job, price queries
internal/fees/       pure fee calculator
internal/ledger/     accounts, Post with balance checks, holdings, Verify
internal/rewards/    request validation and hashing, Create, Reverse
internal/portfolio/  today, portfolio, stats, history
internal/corporate/  splits, mergers, delistings
internal/users/      user existence and creation
internal/money/      decimal formatting and parsing rules
internal/api/        gin routes and handlers
internal/middleware/ logrus request logger
internal/testdb/     integration test database helper
```

Handlers only parse requests, call a service and map errors to status codes.
All business logic is in the services.

## Tests

```sh
cd stocky
go vet ./...
go test ./...                                   # unit tests; DB tests skip
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/assignment_test?sslmode=disable' \
  go test ./...                                 # everything, against Postgres
```

Integration tests truncate `assignment_test` between tests, and hold a Postgres
advisory lock per test binary so packages running in parallel take turns.
The price job is tested against `httptest` servers returning 200, 503 and a
closed port.
