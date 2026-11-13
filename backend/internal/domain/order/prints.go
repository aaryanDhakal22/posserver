package order

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Print results from the Pi (agent contract §2.10ag, main §2.5zn): the floor
// event order.printed reports what one print job of an order actually
// printed, per ticket and printer. main keeps each job with the order
// (order_prints, append only, idempotent on the event id); Store › Orders'
// panel shows them ("Pizza ✓ · Receipt → rerouted to Online").

// PrintTicket is one ticket of a print job.
type PrintTicket struct {
	// Kind: station | receipt | delivery_copies | reprint | void | change |
	// add_on (a newer Pi's other kinds are kept as sent).
	Kind string `json:"kind"`
	// Printer: the printer it was meant for ("" = none at all).
	Printer string `json:"printer"`
	// Result: printed | rerouted | failed | skipped.
	Result    string    `json:"result"`
	PrintedOn string    `json:"printed_on,omitempty"`
	By        string    `json:"by,omitempty"` // rerouted: rule | auto
	Attempts  int       `json:"attempts"`
	Error     string    `json:"error,omitempty"`
	At        time.Time `json:"at"`
}

// OrderPrint is one print job of an order as main keeps it.
type OrderPrint struct {
	EventID string        `json:"event_id"`
	Mode    string        `json:"mode"`
	At      time.Time     `json:"at"`
	Tickets []PrintTicket `json:"tickets"`
}

// Print results (PrintTicket.Result).
var PrintResults = map[string]bool{"printed": true, "rerouted": true, "failed": true, "skipped": true}

// MaxPrintTickets bounds one job (a store has a handful of printers).
const MaxPrintTickets = 50

// CheckPrintJob validates an order.printed job: a mode, 1–50 tickets with a
// kind and a known result (a rerouted one names where it printed), names
// and errors of bounded length; at defaults to the event's. Texts are trimmed.
func CheckPrintJob(mode string, tickets []PrintTicket, at time.Time) (string, []PrintTicket, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" || len(mode) > 32 {
		return "", nil, fmt.Errorf("mode is 1-32 characters")
	}
	if len(tickets) == 0 || len(tickets) > MaxPrintTickets {
		return "", nil, fmt.Errorf("1-%d tickets", MaxPrintTickets)
	}
	out := make([]PrintTicket, 0, len(tickets))
	for i, t := range tickets {
		t.Kind, t.Printer, t.PrintedOn, t.By = strings.TrimSpace(t.Kind), strings.TrimSpace(t.Printer),
			strings.TrimSpace(t.PrintedOn), strings.TrimSpace(t.By)
		switch {
		case t.Kind == "" || len(t.Kind) > 32:
			return "", nil, fmt.Errorf("ticket %d: kind is 1-32 characters", i)
		case !PrintResults[t.Result]:
			return "", nil, fmt.Errorf("ticket %d: result is printed, rerouted, failed or skipped", i)
		case t.Result == "rerouted" && t.PrintedOn == "":
			return "", nil, fmt.Errorf("ticket %d: a rerouted ticket names printed_on", i)
		case utf8.RuneCountInString(t.Printer) > 120 || utf8.RuneCountInString(t.PrintedOn) > 120 || len(t.By) > 16:
			return "", nil, fmt.Errorf("ticket %d: a name is too long", i)
		case t.Attempts < 0 || t.Attempts > 100:
			return "", nil, fmt.Errorf("ticket %d: attempts is 0-100", i)
		}
		if r := []rune(t.Error); len(r) > 300 {
			t.Error = string(r[:300])
		}
		if t.At.IsZero() {
			t.At = at
		}
		out = append(out, t)
	}
	return mode, out, nil
}
