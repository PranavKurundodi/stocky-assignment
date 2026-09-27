package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/corporate"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/money"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/users"
)

// feesJSON and rewardJSON are the wire format of a reward. Every decimal is
// a fixed-precision string and every time is RFC3339 in IST.
type feesJSON struct {
	Brokerage string `json:"brokerage"`
	STT       string `json:"stt"`
	Exchange  string `json:"exchange"`
	SEBI      string `json:"sebi"`
	StampDuty string `json:"stamp_duty"`
	GST       string `json:"gst"`
	Total     string `json:"total"`
}

type rewardJSON struct {
	RewardID   string   `json:"reward_id"`
	UserID     string   `json:"user_id"`
	Symbol     string   `json:"symbol"`
	Quantity   string   `json:"quantity"`
	Reason     string   `json:"reason"`
	RewardedAt string   `json:"rewarded_at"`
	PriceUsed  string   `json:"price_used"`
	PriceAsOf  string   `json:"price_as_of"`
	CostINR    string   `json:"cost_inr"`
	Fees       feesJSON `json:"fees"`
	Status     string   `json:"status"`
	ReversedAt *string  `json:"reversed_at"`
}

func toRewardJSON(r rewards.Reward) rewardJSON {
	var reversedAt *string
	if r.ReversedAt != nil {
		s := timeutil.Format(*r.ReversedAt)
		reversedAt = &s
	}
	return rewardJSON{
		RewardID:   r.ID.String(),
		UserID:     r.UserID,
		Symbol:     r.Symbol,
		Quantity:   money.Qty(r.Quantity),
		Reason:     r.Reason,
		RewardedAt: timeutil.Format(r.RewardedAt),
		PriceUsed:  money.INR(r.PriceUsed),
		PriceAsOf:  timeutil.Format(r.PriceAsOf),
		CostINR:    money.INR(r.CostINR),
		Fees: feesJSON{
			Brokerage: money.INR(r.Fees.Brokerage),
			STT:       money.INR(r.Fees.STT),
			Exchange:  money.INR(r.Fees.Exchange),
			SEBI:      money.INR(r.Fees.SEBI),
			StampDuty: money.INR(r.Fees.StampDuty),
			GST:       money.INR(r.Fees.GST),
			Total:     money.INR(r.Fees.Total),
		},
		Status:     r.Status,
		ReversedAt: reversedAt,
	}
}

// createReward answers POST /reward.
func (h *handlers) createReward(c *gin.Context) {
	var req rewards.Request
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		badRequest(c, "request body must be valid JSON")
		return
	}

	in, err := rewards.Prepare(c.GetHeader("Idempotency-Key"), req, h.Rewards.Now())
	if err != nil {
		h.writeError(c, err)
		return
	}

	rw, created, err := h.Rewards.Create(c.Request.Context(), in)
	if err != nil {
		// Add the ids the client sent, so a 404 says which one was unknown.
		h.writeError(c, err, "user_id", in.UserID, "symbol", in.Symbol)
		return
	}

	status := http.StatusCreated
	if !created {
		status = http.StatusOK // replay of an earlier identical request
	}
	c.JSON(status, toRewardJSON(rw))
}

// badRequest writes the common 400 error shape.
func badRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": msg})
}

// writeError maps service errors to status codes, with the common body
// {"error": "...", ...extra}. extra is key/value pairs; only the one that
// is relevant to the error is included.
func (h *handlers) writeError(c *gin.Context, err error, extra ...string) {
	body := gin.H{"error": err.Error()}
	add := func(key string) {
		for i := 0; i+1 < len(extra); i += 2 {
			if extra[i] == key {
				body[key] = extra[i+1]
			}
		}
	}

	var ve *rewards.ValidationError
	var cve *corporate.ValidationError
	switch {
	case errors.As(err, &ve), errors.As(err, &cve), errors.Is(err, users.ErrInvalid):
		c.JSON(http.StatusBadRequest, body)
	case errors.Is(err, users.ErrExists):
		add("user_id")
		c.JSON(http.StatusConflict, body)
	case errors.Is(err, corporate.ErrUnknownStock):
		c.JSON(http.StatusNotFound, body)
	case errors.Is(err, corporate.ErrNotActive), errors.Is(err, rewards.ErrCannotReverse):
		add("reward_id")
		c.JSON(http.StatusUnprocessableEntity, body)
	case errors.Is(err, users.ErrNotFound):
		add("user_id")
		c.JSON(http.StatusNotFound, body)
	case errors.Is(err, rewards.ErrUnknownStock):
		add("symbol")
		c.JSON(http.StatusNotFound, body)
	case errors.Is(err, rewards.ErrNotFound):
		add("reward_id")
		c.JSON(http.StatusNotFound, body)
	case errors.Is(err, rewards.ErrDelisted):
		add("symbol")
		c.JSON(http.StatusUnprocessableEntity, body)
	case errors.Is(err, rewards.ErrNoPrice):
		add("symbol")
		c.JSON(http.StatusUnprocessableEntity, body)
	case errors.Is(err, rewards.ErrStalePrice):
		add("symbol")
		c.JSON(http.StatusServiceUnavailable, body)
	case errors.Is(err, rewards.ErrIdempotencyConflict):
		c.JSON(http.StatusConflict, body)
	default:
		// Unexpected: log the detail, return nothing internal to the client.
		h.Log.WithError(err).WithField("path", c.Request.URL.Path).Error("request failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}
