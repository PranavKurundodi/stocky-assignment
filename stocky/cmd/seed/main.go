// Command seed fills the database with demo data: three users, 30 days of
// hourly prices, and about 15 backdated rewards.
//
//	go run ./cmd/seed          add demo data (safe to run again)
//	go run ./cmd/seed --reset  wipe all data first, then add it
//
// Rewards are created through the rewards service, exactly as POST /reward
// would, so each one has its fees and balanced ledger entries.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"time"
	_ "time/tzdata" // embed zone data so Asia/Kolkata loads without system tz files

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/config"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/fees"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/users"
)

func main() {
	reset := flag.Bool("reset", false, "delete all users, prices, rewards and ledger rows, and any non-base stocks, before seeding")
	flag.Parse()

	log := logrus.StandardLogger()
	if err := timeutil.Init(); err != nil {
		log.WithError(err).Fatal("cannot load Asia/Kolkata time zone")
	}
	log.SetFormatter(&timeutil.LogFormatter{
		Inner: &logrus.TextFormatter{FullTimestamp: true, TimestampFormat: time.RFC3339},
	})

	// Same config rules as the server: an optional .env in the current directory.
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(".env"); err != nil {
			log.WithError(err).Fatal("cannot parse .env")
		}
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.WithError(err).Fatal("invalid configuration")
	}
	log.SetLevel(cfg.LogLevel)

	ctx := context.Background()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.WithError(err).Fatal("cannot connect to database")
	}
	defer pool.Close()

	// The seed may run before the server ever has, so make sure the schema exists.
	if _, err := db.MigrateUp(cfg.DatabaseURL); err != nil {
		log.WithError(err).Fatal("cannot apply migrations")
	}

	if *reset {
		if err := db.ResetData(ctx, pool); err != nil {
			log.WithError(err).Fatal("reset failed")
		}
		log.Warn("reset: all users, prices, rewards and ledger rows deleted; stocks back to the five base stocks, all ACTIVE")
	}

	now := time.Now()

	// Users.
	created := 0
	for _, u := range seedUsers {
		err := users.Create(ctx, pool, u.id, u.name)
		switch {
		case err == nil:
			created++
		case errors.Is(err, users.ErrExists):
			// already there from an earlier run
		default:
			log.WithError(err).WithField("user_id", u.id).Fatal("create user")
		}
	}
	log.WithFields(logrus.Fields{"created": created, "existing": len(seedUsers) - created}).Info("users seeded")

	// Prices, in one transaction. ON CONFLICT makes a rerun a no-op.
	prices := generatePrices(now)
	inserted := 0
	err = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, p := range prices {
			batch.Queue(`INSERT INTO stock_prices (symbol, price, as_of, fetched_at)
			             VALUES ($1, $2, $3, $3) ON CONFLICT (symbol, as_of) DO NOTHING`,
				p.symbol, p.price, p.asOf)
		}
		results := tx.SendBatch(ctx, batch)
		defer results.Close()
		for range prices {
			tag, err := results.Exec()
			if err != nil {
				return err
			}
			inserted += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		log.WithError(err).Fatal("insert prices")
	}
	log.WithFields(logrus.Fields{
		"inserted": inserted, "skipped": len(prices) - inserted,
		"from": timeutil.Format(prices[0].asOf), "to": timeutil.Format(prices[len(prices)-1].asOf),
	}).Info("prices seeded")

	// Rewards, through the same service POST /reward uses.
	svc := rewards.NewService(pool, fees.DefaultRates(cfg.FeeBrokerageFlat), cfg.PriceStaleAfter, log)
	var made, replayed, failed int
	for _, r := range generateRewards(now) {
		ts := r.rewardedAt.Format(time.RFC3339)
		in, err := rewards.Prepare(r.key, rewards.Request{
			UserID: r.userID, Symbol: r.symbol, Quantity: json.RawMessage(`"` + r.quantity + `"`),
			Reason: r.reason, RewardedAt: &ts,
		}, now)
		if err == nil {
			var isNew bool
			_, isNew, err = svc.Create(ctx, in)
			if err == nil && isNew {
				made++
				continue
			}
			if err == nil {
				replayed++
				continue
			}
		}
		failed++
		log.WithError(err).WithFields(logrus.Fields{"key": r.key, "symbol": r.symbol}).Warn("seed reward not created")
	}
	log.WithFields(logrus.Fields{"created": made, "replayed": replayed, "failed": failed}).Info("rewards seeded")
	log.Info("seed complete")
}
