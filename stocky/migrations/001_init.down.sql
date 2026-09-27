-- Reverse of 001_init.up.sql, dropping tables children first.
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS ledger_transactions;
DROP TABLE IF EXISTS corporate_actions;
DROP TABLE IF EXISTS reward_events;
DROP TABLE IF EXISTS stock_prices;
DROP TABLE IF EXISTS stocks;
DROP TABLE IF EXISTS users;
DROP FUNCTION IF EXISTS forbid_update_delete();
