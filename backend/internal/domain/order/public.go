package order

import "strings"

// Public is the status a sales channel and a customer see (main 3.0):
// received | scheduled | accepted | ready | on_delivery | completed | cancelled.
func (s Status) Public() string {
	switch s {
	case StatusReceived, StatusValidated:
		return "received"
	case StatusAccepted, StatusPreparing:
		return "accepted"
	case StatusRejected:
		return "cancelled"
	}
	return string(s)
}

// FirstName is the part of a name a customer may see (a driver: "Abdul").
func FirstName(name string) string {
	if f := strings.Fields(name); len(f) > 0 {
		return f[0]
	}
	return ""
}
