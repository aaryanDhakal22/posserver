package order

import "github.com/google/uuid"

// The counter's money / permission rules as main checks them (the owner,
// 2026-11-12: "make main on the same page for rules as the Pi and
// terminal"). The Pi enforces them (agent contract 2.10 Permissions, 2.10n,
// 2.10s, 2.10x); main stores every money event it can and flags what the Pi
// would not have allowed: a manager's approval the Pi would have asked for
// and nobody gave, or money the Pi would have refused. One table for the
// three: quicc-ops contract/money-rules.json (testdata/money-rules.json here,
// a byte copy; TestMoneyRules runs every case).

// FlagCompNeedsManager: a driver's comp nobody approved (a cashier or a
// manager comps freely, the owner 2026-11-12).
const FlagCompNeedsManager = "comp_needs_manager"

// Refunder is who took or gave a counter payment, as main's staff table
// says. Known false = no such person at the store (no staff_id, or they are
// gone): main can't tell who it was, so it raises no permission flag for
// them, and their earlier refunds count against a cashier's limit (the
// Pi's rule).
type Refunder struct {
	Known     bool
	Role      string // cashier | manager | driver
	Unlimited bool   // a manager, or main's effective refund limit is null
	Limit     int64  // the effective refund limit (cents) when not unlimited
}

// RefunderOf is a person by role and effective refund limit (nil =
// unlimited). A manager is always unlimited.
func RefunderOf(role string, limit *int64) Refunder {
	r := Refunder{Known: true, Role: role, Unlimited: limit == nil || role == "manager"}
	if !r.Unlimited {
		r.Limit = *limit
	}
	return r
}

// IsCardMethod: money on a card (the Square Reader, Square's app, a
// manually entered card): only a manager gives it back.
func IsCardMethod(m string) bool { return m == "card" || m == "square_app" || m == "manual_card" }

// IsCompMethod: a comp moves no money (order.payment says comp; an
// order.placed snapshot's summary says other).
func IsCompMethod(m string) bool { return m == "comp" || m == "other" }

// PriorPayment is one payment the order already has, as main stores it (a
// refund negative with its tip folded in).
type PriorPayment struct {
	Method     string
	Amount     int64
	Tip        int64
	ApprovedBy string
	By         Refunder
}

// RuleOrder is the order a counter payment lands on.
type RuleOrder struct {
	Total     int64
	Cancelled bool
	Prior     []PriorPayment
}

// PaymentFlags are the flags main stores on a counter payment (order.payment,
// and each payment of an order.placed), besides the refund_of ones the
// repository checks itself. by = who took / gave it.
//
// A payment: order_cancelled, overpaid; a driver's comp nobody approved:
// comp_needs_manager. A refund (negative): more back than the order holds →
// refund_exceeds_paid alone (the Pi refuses it: refund_too_big); more comp
// back than was comped → refund_exceeds_tender (refused too); else, when
// nobody approved it and a person with limits gave it — card →
// refund_needs_manager (the Pi's card_refund), cash past the order's cash →
// refund_exceeds_tender (cash_for_card), cash past the person's limit
// counting every earlier refund on the order no manager approved and no
// unlimited person gave, comps aside → over_staff_limit (refund). A comp
// reversed is free.
func PaymentFlags(o RuleOrder, p CheckedPayment, by Refunder) []MoneyFlag {
	amount, tip := int64(p.Amount), int64(p.Tip)
	var flags []MoneyFlag
	var paidAmount, paidNet, tenderNet, cashierRefunded int64
	for _, x := range o.Prior {
		if !CounterMethods[x.Method] {
			continue // not the order's money (a driver's door tip)
		}
		paidAmount += x.Amount
		paidNet += x.Amount + x.Tip
		if x.Method == p.Method || (IsCompMethod(x.Method) && IsCompMethod(p.Method)) {
			tenderNet += x.Amount + x.Tip
		}
		if x.Amount < 0 && x.ApprovedBy == "" && !IsCompMethod(x.Method) && !x.By.Unlimited {
			cashierRefunded += -x.Amount
		}
	}
	limited := by.Known && !by.Unlimited && p.ApprovedBy == ""
	if amount >= 0 {
		if amount > 0 && o.Cancelled {
			flags = append(flags, MoneyFlag{Code: FlagOrderCancelled})
		}
		if amount > 0 && paidAmount+amount > o.Total {
			flags = append(flags, MoneyFlag{Code: FlagOverpaid, Detail: map[string]any{"paid_cents": paidAmount + amount, "total_cents": o.Total}})
		}
		if amount > 0 && IsCompMethod(p.Method) && limited && by.Role != "cashier" && by.Role != "manager" {
			flags = append(flags, MoneyFlag{Code: FlagCompNeedsManager, Detail: map[string]any{"role": by.Role, "comp_cents": amount}})
		}
		return flags
	}
	back := -amount + tip // tip is 0 on the Pi's refunds (folded in)
	switch {
	case paidNet-back < 0:
		return append(flags, MoneyFlag{Code: FlagRefundExceedsPaid, Detail: map[string]any{"paid_cents": paidNet, "refund_cents": back}})
	case IsCompMethod(p.Method):
		if tenderNet-back < 0 {
			flags = append(flags, MoneyFlag{Code: FlagRefundExceedsTender, Detail: map[string]any{"tender": p.Method,
				"tender_net_cents": tenderNet, "refund_cents": back}})
		}
		return flags
	case !limited:
		return flags
	case IsCardMethod(p.Method):
		return append(flags, MoneyFlag{Code: FlagRefundNeedsManager, Detail: map[string]any{"role": by.Role, "method": p.Method}})
	case tenderNet-back < 0:
		return append(flags, MoneyFlag{Code: FlagRefundExceedsTender, Detail: map[string]any{"tender": p.Method,
			"tender_net_cents": tenderNet, "refund_cents": back}})
	case cashierRefunded+back > by.Limit:
		return append(flags, MoneyFlag{Code: FlagOverStaffLimit, Detail: map[string]any{"limit_cents": by.Limit,
			"refunded_cents": cashierRefunded + back}})
	}
	return flags
}

// GeneralRefundFlags are the flags main stores on a general refund (cash
// back with no order, agent 2.10x): a manager every time, so one a
// non-manager gave with nobody approving is refund_needs_manager (one flag,
// whatever the amount: the Pi's single answer).
func GeneralRefundFlags(p CheckedPayment, by Refunder) []MoneyFlag {
	if !by.Known || by.Role == "manager" || p.ApprovedBy != "" {
		return nil
	}
	return []MoneyFlag{{Code: FlagRefundNeedsManager, Detail: map[string]any{"role": by.Role, "general": true}}}
}

// Who is the refunder a payment names (unknown when it names nobody main
// knows).
func Who(people map[uuid.UUID]Refunder, id *uuid.UUID) Refunder {
	if id == nil {
		return Refunder{}
	}
	return people[*id]
}
