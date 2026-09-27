package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/corporate"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/ledger"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/money"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/rewards"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/timeutil"
	"github.com/PranavKurundodi/stocky-assignment/stocky/internal/users"
)

// createUser answers POST /users.
func (h *handlers) createUser(c *gin.Context) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		badRequest(c, "request body must be valid JSON")
		return
	}
	id, name := strings.TrimSpace(body.ID), strings.TrimSpace(body.Name)
	if err := users.Create(c.Request.Context(), h.Pool, id, name); err != nil {
		h.writeError(c, err, "user_id", id)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "name": name})
}

// reverseReward answers POST /reward/:id/reverse.
func (h *handlers) reverseReward(c *gin.Context) {
	raw := c.Param("id")
	id, err := uuid.Parse(raw)
	if err != nil {
		// Not a UUID, so it cannot be a reward id.
		h.writeError(c, rewards.ErrNotFound, "reward_id", raw)
		return
	}
	rw, err := h.Rewards.Reverse(c.Request.Context(), id)
	if err != nil {
		h.writeError(c, err, "reward_id", raw)
		return
	}
	c.JSON(http.StatusOK, toRewardJSON(rw))
}

// createCorporateAction answers POST /admin/corporate-actions.
func (h *handlers) createCorporateAction(c *gin.Context) {
	var req corporate.Request
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		badRequest(c, "request body must be valid JSON")
		return
	}
	action, err := h.Corporate.Prepare(req)
	if err != nil {
		h.writeError(c, err)
		return
	}
	action, err = h.Corporate.Apply(c.Request.Context(), action)
	if err != nil {
		h.writeError(c, err)
		return
	}

	body := gin.H{
		"id":               action.ID.String(),
		"type":             action.Type,
		"symbol":           action.Symbol,
		"new_symbol":       nil,
		"ratio_from":       nil,
		"ratio_to":         nil,
		"effective_at":     timeutil.Format(action.EffectiveAt),
		"holders_affected": action.HoldersAffected,
	}
	if action.NewSymbol != "" {
		body["new_symbol"] = action.NewSymbol
	}
	if action.Type != corporate.Delisting {
		body["ratio_from"] = money.Qty(action.RatioFrom)
		body["ratio_to"] = money.Qty(action.RatioTo)
	}
	c.JSON(http.StatusCreated, body)
}

// verifyLedger answers GET /admin/ledger/verify.
func (h *handlers) verifyLedger(c *gin.Context) {
	bad, checked, err := ledger.Verify(c.Request.Context(), h.Pool)
	if err != nil {
		h.writeError(c, err)
		return
	}
	type row struct {
		TransactionID string `json:"transaction_id"`
		Asset         string `json:"asset"`
		Debits        string `json:"debits"`
		Credits       string `json:"credits"`
	}
	out := make([]row, len(bad))
	for i, b := range bad {
		format := money.Qty
		if b.Asset == ledger.AssetINR {
			format = money.INR
		}
		out[i] = row{b.TransactionID.String(), b.Asset, format(b.Debits), format(b.Credits)}
	}
	c.JSON(http.StatusOK, gin.H{"unbalanced": out, "checked": checked})
}
