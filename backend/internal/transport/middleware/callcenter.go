package middleware

import "context"

// CallcenterActor is who a /callcenter/v1 request is: the browser (device)
// and, once signed in, the agent. The audit trail reads it.
type CallcenterActor struct {
	DeviceID   string
	DeviceName string
	StaffID    string
	StaffName  string
	TenantID   string
	LocationID string
}

type callcenterKeyT struct{}

// WithCallcenter marks the request as a call-center device's.
func WithCallcenter(ctx context.Context, a CallcenterActor) context.Context {
	return context.WithValue(ctx, callcenterKeyT{}, a)
}

// CallcenterFromContext is the request's call-center device, if any.
func CallcenterFromContext(ctx context.Context) (CallcenterActor, bool) {
	a, ok := ctx.Value(callcenterKeyT{}).(CallcenterActor)
	return a, ok
}
