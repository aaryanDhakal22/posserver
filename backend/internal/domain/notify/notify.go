// Package notify is the notification port (main 3.0 step 6): main tells
// someone that something happened. Today: problems with channel orders go to
// the store owner (Pushover) and the log; customer messages (SMS / email)
// plug in later through the same port.
package notify

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Notice kinds.
const (
	KindOrderNotAccepted = "order_not_accepted" // a channel order the Pi hasn't accepted in time
	KindRefundFailed     = "refund_failed"      // a charge couldn't be refunded after a failed save
)

// Notice is one message about a store.
type Notice struct {
	LocationID uuid.UUID
	Kind       string
	Title      string
	Message    string
}

// Notifier delivers notices.
type Notifier interface {
	Notify(ctx context.Context, n Notice) error
}

// Log writes every notice to the log (always on).
type Log struct{ Logger zerolog.Logger }

func (l Log) Notify(ctx context.Context, n Notice) error {
	l.Logger.Warn().Ctx(ctx).Str("kind", n.Kind).Str("location_id", n.LocationID.String()).Str("title", n.Title).Msg(n.Message)
	return nil
}

// Fanout delivers to every notifier; the first error is returned (the rest
// still get it).
type Fanout []Notifier

func (f Fanout) Notify(ctx context.Context, n Notice) error {
	var first error
	for _, x := range f {
		if err := x.Notify(ctx, n); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Pager is the Pushover client (infra/pushover): SendAlso pages the admin's
// devices plus extra ones (the store owner's phones).
type Pager interface {
	Enabled() bool
	SendAlso(ctx context.Context, extra []string, title, message, sound string) error
}

// OwnerDevices are a location's owner phones (tenancy config).
type OwnerDevices func(ctx context.Context, locationID uuid.UUID) ([]string, error)

// Pushover pages the store owner's phones (and the admin's).
type Pushover struct {
	Pager   Pager
	Devices OwnerDevices
}

var ErrNotConfigured = errors.New("pushover isn't configured")

func (p Pushover) Notify(ctx context.Context, n Notice) error {
	if p.Pager == nil || !p.Pager.Enabled() {
		return ErrNotConfigured
	}
	var devices []string
	if p.Devices != nil {
		d, err := p.Devices(ctx, n.LocationID)
		if err != nil {
			return err
		}
		devices = d
	}
	return p.Pager.SendAlso(ctx, devices, n.Title, n.Message, "siren")
}
