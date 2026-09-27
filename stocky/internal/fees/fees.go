// Package fees computes the charges Stocky pays when it buys shares to give
// to a user. It is pure arithmetic: no database, no network, no clock.
//
// The default rates are modelled on NSE equity delivery buy charges. They
// change from time to time and should be verified against a current broker
// charge sheet before being relied on.
package fees

import "github.com/shopspring/decimal"

// Places is the precision of every INR amount.
const Places = 4

// Rates are the fee parameters. Percentages are stored as fractions, so
// 0.1% is 0.001.
type Rates struct {
	BrokerageFlat decimal.Decimal // flat INR per trade (default 20; some brokers charge 0 for delivery)
	STT           decimal.Decimal // Securities Transaction Tax, on trade value
	Exchange      decimal.Decimal // exchange transaction charge, on trade value
	SEBI          decimal.Decimal // SEBI turnover fee, on trade value
	StampDuty     decimal.Decimal // stamp duty on the buy side, on trade value
	GST           decimal.Decimal // GST, on brokerage + exchange + SEBI
}

// DefaultRates returns the standard rates with the given flat brokerage
// (FEE_BROKERAGE_FLAT from config).
func DefaultRates(brokerageFlat decimal.Decimal) Rates {
	return Rates{
		BrokerageFlat: brokerageFlat,
		STT:           decimal.RequireFromString("0.001"),     // 0.1%
		Exchange:      decimal.RequireFromString("0.0000297"), // 0.00297%
		SEBI:          decimal.RequireFromString("0.000001"),  // 0.0001%
		StampDuty:     decimal.RequireFromString("0.00015"),   // 0.015%
		GST:           decimal.RequireFromString("0.18"),      // 18%
	}
}

// Breakdown is every fee component for one trade, each already rounded to
// 4 decimal places, plus their total.
type Breakdown struct {
	Brokerage decimal.Decimal
	STT       decimal.Decimal
	Exchange  decimal.Decimal
	SEBI      decimal.Decimal
	StampDuty decimal.Decimal
	GST       decimal.Decimal
	Total     decimal.Decimal
}

// round rounds to 4 dp, half away from zero. Fees are never negative, so
// that is the same as half-up.
func round(d decimal.Decimal) decimal.Decimal {
	return d.Round(Places)
}

// Calculate returns the fees for buying shares worth tradeValue INR.
//
// Each component is rounded on its own, and the total is the sum of the
// rounded components. That way the ledger lines (one per component) always
// add up exactly to the cash that left COMPANY_CASH, with no rounding gap.
func (r Rates) Calculate(tradeValue decimal.Decimal) Breakdown {
	var b Breakdown
	b.Brokerage = round(r.BrokerageFlat)
	b.STT = round(tradeValue.Mul(r.STT))
	b.Exchange = round(tradeValue.Mul(r.Exchange))
	b.SEBI = round(tradeValue.Mul(r.SEBI))
	b.StampDuty = round(tradeValue.Mul(r.StampDuty))
	// GST is charged on the service fees as they appear on the contract
	// note, i.e. on the already rounded components.
	b.GST = round(b.Brokerage.Add(b.Exchange).Add(b.SEBI).Mul(r.GST))

	b.Total = b.Brokerage.Add(b.STT).Add(b.Exchange).Add(b.SEBI).Add(b.StampDuty).Add(b.GST)
	return b
}
