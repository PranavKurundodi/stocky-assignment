package rewards

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/sirupsen/logrus"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/db"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/money"
)

// ErrCannotReverse means the reward's shares were changed by a split or a
// merger after it was given, so "take back the original quantity" no longer
// describes what the user holds. Such cases need a manual adjustment.
var ErrCannotReverse = errors.New("reward cannot be reversed after a split or merger of its stock")

// Reverse takes a reward's shares back from the user. It is idempotent:
// reversing an already reversed reward returns it unchanged and writes
// nothing.
//
// A reversal is not a mirror of the reward. Stocky already bought the
// shares and paid the fees; neither comes back. The shares move into
// Stocky's own inventory and their original cost moves out of expense:
//
//	Dr COMPANY_STOCK         SYM  quantity
//	Cr USER_STOCK(user)      SYM  quantity
//	Dr STOCK_INVENTORY_VALUE INR  original cost
//	Cr REWARD_EXPENSE        INR  original cost
func (s *Service) Reverse(ctx context.Context, id uuid.UUID) (Reward, error) {
	reversed := false
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// FOR UPDATE locks the reward row. A second concurrent reversal
		// waits here until we commit, then sees REVERSED and does nothing.
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM reward_events WHERE id = $1 FOR UPDATE`, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == StatusReversed {
			return nil
		}

		rw, err := load(ctx, tx, `WHERE id = $1`, id)
		if err != nil {
			return err
		}

		// A split or merger adjusts balances as they stand when it is
		// recorded, so compare when each row was written (created_at), not
		// effective_at: an action recorded after this reward was booked has
		// already changed these shares, whatever date it is effective from.
		var changed bool
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (
			  SELECT 1
			  FROM corporate_actions ca
			  JOIN reward_events r ON r.id = $1
			  WHERE ca.symbol = r.symbol
			    AND ca.type IN ('SPLIT', 'MERGER')
			    AND ca.created_at > r.created_at)`,
			rw.ID).Scan(&changed)
		if err != nil {
			return err
		}
		if changed {
			return ErrCannotReverse
		}

		now := s.Now()
		_, err = ledger.Post(ctx, tx, ledger.Transaction{
			Type:        ledger.TypeRewardReversal,
			RewardID:    &rw.ID,
			EffectiveAt: now,
			Description: fmt.Sprintf("reversal of reward %s", rw.ID),
			Entries: []ledger.Entry{
				ledger.Dr(ledger.CompanyStock, rw.Symbol, rw.Quantity),
				ledger.Cr(ledger.UserStock, rw.Symbol, rw.Quantity).ForUser(rw.UserID),
				ledger.Dr(ledger.StockInventoryValue, ledger.AssetINR, rw.CostINR),
				ledger.Cr(ledger.RewardExpense, ledger.AssetINR, rw.CostINR),
			},
		})
		if err != nil {
			return err
		}

		// The reward row itself is not ledger data; its status may change.
		_, err = tx.Exec(ctx, `UPDATE reward_events SET status = 'REVERSED', reversed_at = $2 WHERE id = $1`, id, now)
		reversed = err == nil
		return err
	})
	if err != nil {
		return Reward{}, err
	}

	rw, err := s.Get(ctx, id)
	if err != nil {
		return Reward{}, err
	}
	if reversed {
		s.log.WithFields(logrus.Fields{
			"reward_id": rw.ID, "user_id": rw.UserID, "symbol": rw.Symbol, "quantity": money.Qty(rw.Quantity),
		}).Info("reward reversed")
	}
	return rw, nil
}
