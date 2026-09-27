-- Stocky schema.
--
-- Money is NUMERIC everywhere: NUMERIC(18,4) for prices and INR amounts,
-- NUMERIC(18,6) for share quantities. Holdings are never stored; they are
-- always derived by summing ledger_entries.

CREATE TABLE users (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE stocks (
    symbol     TEXT PRIMARY KEY,
    status     TEXT NOT NULL CHECK (status IN ('ACTIVE', 'DELISTED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO stocks (symbol, status) VALUES
    ('RELIANCE', 'ACTIVE'),
    ('TCS', 'ACTIVE'),
    ('INFY', 'ACTIVE'),
    ('ICICIBANK', 'ACTIVE'),
    ('HDFCBANK', 'ACTIVE');

-- Every quote ever fetched. Append only: a price is never corrected in place,
-- because past valuations (historical-inr) must stay reproducible.
CREATE TABLE stock_prices (
    id         BIGSERIAL PRIMARY KEY,
    symbol     TEXT NOT NULL REFERENCES stocks (symbol),
    price      NUMERIC(18, 4) NOT NULL CHECK (price > 0),
    as_of      TIMESTAMPTZ NOT NULL,  -- when the vendor says the price was valid
    fetched_at TIMESTAMPTZ NOT NULL,  -- when we fetched it
    -- Fetching the same vendor quote twice is harmless: ON CONFLICT skips it.
    UNIQUE (symbol, as_of)
);
-- "Latest price at or before t" lookups walk this index backwards from t.
CREATE INDEX stock_prices_symbol_as_of_desc ON stock_prices (symbol, as_of DESC);

CREATE TABLE reward_events (
    id              UUID PRIMARY KEY,
    -- The UNIQUE constraint is what makes POST /reward idempotent, even when
    -- two identical requests race each other.
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash    TEXT NOT NULL,
    user_id         TEXT NOT NULL REFERENCES users (id),
    symbol          TEXT NOT NULL REFERENCES stocks (symbol),
    quantity        NUMERIC(18, 6) NOT NULL CHECK (quantity > 0),
    reason          TEXT NOT NULL CHECK (reason IN ('ONBOARDING', 'REFERRAL', 'MILESTONE', 'OTHER')),
    rewarded_at     TIMESTAMPTZ NOT NULL,
    price_used      NUMERIC(18, 4) NOT NULL,
    price_as_of     TIMESTAMPTZ NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('ACTIVE', 'REVERSED')),
    reversed_at     TIMESTAMPTZ NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- reversed_at is set exactly when the reward is reversed.
    CHECK ((status = 'REVERSED') = (reversed_at IS NOT NULL))
);
CREATE INDEX reward_events_user_rewarded_at ON reward_events (user_id, rewarded_at);

CREATE TABLE corporate_actions (
    id           UUID PRIMARY KEY,
    type         TEXT NOT NULL CHECK (type IN ('SPLIT', 'MERGER', 'DELISTING')),
    symbol       TEXT NOT NULL REFERENCES stocks (symbol),
    new_symbol   TEXT NULL REFERENCES stocks (symbol),
    ratio_from   NUMERIC(18, 6) NULL CHECK (ratio_from > 0),
    ratio_to     NUMERIC(18, 6) NULL CHECK (ratio_to > 0),
    effective_at TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Splits and mergers need a ratio; only mergers have a new symbol.
    CHECK (type = 'DELISTING' OR (ratio_from IS NOT NULL AND ratio_to IS NOT NULL)),
    CHECK ((type = 'MERGER') = (new_symbol IS NOT NULL))
);

CREATE TABLE ledger_transactions (
    id                  UUID PRIMARY KEY,
    type                TEXT NOT NULL CHECK (type IN ('REWARD', 'REWARD_REVERSAL', 'SPLIT', 'MERGER')),
    reward_id           UUID NULL REFERENCES reward_events (id),
    corporate_action_id UUID NULL REFERENCES corporate_actions (id),
    -- Economic time. All holdings math filters on this, never on created_at,
    -- so a backdated reward counts from the day it was earned.
    effective_at        TIMESTAMPTZ NOT NULL,
    description         TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entries (
    id             BIGSERIAL PRIMARY KEY,
    transaction_id UUID NOT NULL REFERENCES ledger_transactions (id),
    account        TEXT NOT NULL,
    user_id        TEXT NULL REFERENCES users (id),
    asset          TEXT NOT NULL,  -- 'INR' or a stock symbol
    direction      TEXT NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    quantity       NUMERIC(18, 6) NULL,
    inr_amount     NUMERIC(18, 4) NULL,
    -- INR lines carry an INR amount; stock lines carry a quantity. Never both.
    CHECK (
        (asset = 'INR' AND inr_amount IS NOT NULL AND quantity IS NULL)
        OR (asset <> 'INR' AND quantity IS NOT NULL AND inr_amount IS NULL)
    ),
    -- Whichever amount is set must be positive; the sign is the direction.
    CHECK (COALESCE(inr_amount, quantity) > 0),
    -- Only user stock accounts belong to a user.
    CHECK ((account = 'USER_STOCK') = (user_id IS NOT NULL))
);
CREATE INDEX ledger_entries_user_asset ON ledger_entries (user_id, asset);
CREATE INDEX ledger_entries_transaction ON ledger_entries (transaction_id);

-- The ledger and the price history are append only. Enforce it in the
-- database too, so no code path (or manual query) can quietly rewrite
-- history. Corrections are new rows. TRUNCATE is still allowed, which the
-- seed --reset command and the tests use.
CREATE FUNCTION forbid_update_delete() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append only: % is not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER ledger_transactions_append_only
    BEFORE UPDATE OR DELETE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();

CREATE TRIGGER ledger_entries_append_only
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();

CREATE TRIGGER stock_prices_append_only
    BEFORE UPDATE OR DELETE ON stock_prices
    FOR EACH ROW EXECUTE FUNCTION forbid_update_delete();
