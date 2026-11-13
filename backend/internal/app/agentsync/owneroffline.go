package agentsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/pushover"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/shared/safego"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The store owner's page for a Pi offline (main 4.0.2, contract §2.5d).
//
// main is the only one that can tell the owner when the store's Pi is down or
// the store's internet is out (the Pi can't send anything then), so main owns
// the owner's offline alert: plain English, inside opening hours (else
// 09:00-02:00), after owner_alert_offline_minutes (5), once per outage plus
// one "back online", with the agent's cooldown and caps. store_config tells
// the Pi so (owner_offline_alert_by_main) and the Pi then skips its own
// "store offline" owner alert: the owner gets one, never two. The admin's
// technical page (2 min, admin devices only) is separate.

// Dedup keys of main's two owner messages.
const (
	ownerKeyOffline = "offline"
	ownerKeyOnline  = "online"
)

type ownerAction int

const (
	ownerNone ownerAction = iota
	ownerSendOffline
	ownerSendOnline
)

func (a ownerAction) String() string {
	return [...]string{"none", "offline", "online"}[a]
}

// errNobody: there is nobody to tell (no owner devices and no boss key, a
// test store, Pushover not configured). The outage counts as dealt with.
var errNobody = errors.New("no owner to page")

// ownerOffline is one location's owner-page state (under Service.offMu).
type ownerOffline struct {
	since    time.Time // first seen offline (zero = connected)
	done     bool      // this outage is dealt with
	told     bool      // its offline message went out (so "back online" is owed)
	inflight bool      // a send is running
	last     map[string]time.Time
	sent     []time.Time // owner messages from main for this store, last 24 h
}

func newOwnerOffline() *ownerOffline { return &ownerOffline{last: map[string]time.Time{}} }

func (o *ownerOffline) prune(now time.Time) {
	kept := o.sent[:0]
	for _, t := range o.sent {
		if now.Sub(t) < 24*time.Hour {
			kept = append(kept, t)
		}
	}
	o.sent = kept
}

func (o *ownerOffline) capped(now time.Time, s tenancy.OwnerAlertSettings) bool {
	hour := 0
	for _, t := range o.sent {
		if now.Sub(t) < time.Hour {
			hour++
		}
	}
	return hour >= s.MaxPerHour || len(o.sent) >= s.MaxPerDay
}

func (o *ownerOffline) cooling(now time.Time, key string, s tenancy.OwnerAlertSettings) bool {
	t, ok := o.last[key]
	return ok && now.Sub(t) < s.Cooldown
}

// decide is one tick: what to send now (if anything) and, when an alert is
// let go, why ("off", "capped", "closed", "cooldown"; "" = nothing to say).
// A returned send is in flight until result.
func (o *ownerOffline) decide(now time.Time, connected, open bool, s tenancy.OwnerAlertSettings) (ownerAction, string) {
	if o.inflight {
		return ownerNone, ""
	}
	o.prune(now)
	if connected {
		o.since, o.done = time.Time{}, false
		if !o.told {
			return ownerNone, ""
		}
		why := ""
		switch {
		case !s.Enabled:
			why = "off"
		case !open:
			why = "closed"
		case o.cooling(now, ownerKeyOnline, s):
			why = "cooldown"
		case o.capped(now, s):
			why = "capped"
		}
		if why != "" { // as on the Pi: no retry for "back online"
			o.told = false
			return ownerNone, why
		}
		o.inflight = true
		return ownerSendOnline, ""
	}
	if o.since.IsZero() {
		o.since = now
	}
	if o.done || now.Sub(o.since) < s.Offline {
		return ownerNone, ""
	}
	switch {
	case !s.Enabled:
		o.done, o.told = true, false
		return ownerNone, "off"
	case !open:
		return ownerNone, "closed" // told when the store opens, if still out
	case o.cooling(now, ownerKeyOffline, s):
		return ownerNone, "cooldown" // told when the cooldown ends, if still out
	case o.capped(now, s):
		o.done, o.told = true, false
		return ownerNone, "capped"
	}
	o.inflight = true
	return ownerSendOffline, ""
}

// result records how a send from decide went: err nil = sent, errNobody =
// nobody to tell (dealt with), any other error = offered again next tick.
func (o *ownerOffline) result(a ownerAction, now time.Time, err error) {
	o.inflight = false
	nobody := errors.Is(err, errNobody)
	switch a {
	case ownerSendOffline:
		switch {
		case err == nil:
			o.done, o.told = true, true
			o.last[ownerKeyOffline] = now
			o.sent = append(o.sent, now)
		case nobody:
			o.done, o.told = true, false
		}
	case ownerSendOnline:
		switch {
		case err == nil:
			o.told = false
			o.last[ownerKeyOnline] = now
			o.sent = append(o.sent, now)
		case nobody:
			o.told = false
		}
	}
}

// storeLabel is the store's name for the owner's message.
func storeLabel(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return "The store's"
}

// ownerOfflineText: the owner's words, no technical terms. since = when main
// first saw the Pi gone, in the store's time.
func ownerOfflineText(store string, since time.Time, tz *time.Location) string {
	if tz == nil {
		tz = time.UTC
	}
	return fmt.Sprintf("%s tablet system went offline at %s. Check the store's internet and power. Online orders will wait until it's back.",
		storeLabel(store), since.In(tz).Format("3:04 PM"))
}

func ownerOnlineText(store string) string {
	n := strings.TrimSpace(store)
	if n == "" {
		n = "The store"
	}
	return n + " is back online. Orders that waited are printing now."
}

// checkOwnerOffline runs main's owner page for one location (every tick).
func (s *Service) checkOwnerOffline(ctx context.Context, l tenancy.Location, tz *time.Location, connected bool, now time.Time) {
	settings := tenancy.OwnerAlertSettingsFrom(l.Config)
	open := tenancy.OwnerAlertsOpen(l.Config, now, tz)
	s.offMu.Lock()
	st := s.owner[l.ID]
	if st == nil {
		st = newOwnerOffline()
		s.owner[l.ID] = st
	}
	act, why := st.decide(now, connected, open, settings)
	since := st.since
	s.offMu.Unlock()
	if why == "capped" || why == "off" {
		s.logger.Info().Ctx(ctx).Str("location_id", l.ID.String()).Str("why", why).Msg("owner offline page not sent")
	}
	if act == ownerNone {
		return
	}
	safego.Go(context.WithoutCancel(ctx), "agentsync.owner_page", func(ctx context.Context) {
		err := errors.New("owner page interrupted")
		defer func() { // even after a panic: never stuck in flight
			s.offMu.Lock()
			st.result(act, now, err)
			s.offMu.Unlock()
		}()
		err = s.sendOwnerOffline(ctx, l, tz, act, since)
		if err != nil && !errors.Is(err, errNobody) {
			s.logger.Warn().Err(err).Str("location_id", l.ID.String()).Str("kind", act.String()).Msg("owner offline page failed (offered again)")
		}
	})
}

func (s *Service) sendOwnerOffline(ctx context.Context, l tenancy.Location, tz *time.Location, act ownerAction, since time.Time) error {
	if s.ownerSend == nil {
		return errNobody
	}
	t, err := s.tenancy.GetTenant(ctx, l.TenantID)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(t.Name)
	if title == "" {
		title = l.Name
	}
	text := ownerOnlineText(t.Name)
	if act == ownerSendOffline {
		text = ownerOfflineText(t.Name, since, tz)
	}
	if t.TestStore { // a throwaway test store never pages anyone's phone
		s.logger.Info().Ctx(ctx).Str("location_id", l.ID.String()).Str("kind", act.String()).Str("text", text).Msg("owner offline page (test store: logged only)")
		return errNobody
	}
	err = s.ownerSend(ctx, tenancy.OwnerDevices(l.Config), title, text, "")
	if errors.Is(err, pushover.ErrNoRecipient) || errors.Is(err, pushover.ErrNotConfigured) {
		return errNobody
	}
	if err == nil {
		s.ownerPages.Add(ctx, 1, observability.WithStore(ctx, l.TenantID, attribute.String("kind", act.String())))
	}
	return err
}

// OwnerOfflineAlertByMain reports whether main pages the store owner when a
// Pi goes offline (its Pushover is configured): store_config then carries
// owner_offline_alert_by_main and the Pi skips its own owner offline alert.
func (s *Service) OwnerOfflineAlertByMain() bool { return s.pager.Enabled() }

// ownerPagesCounter is the owner page metric.
func ownerPagesCounter(m metric.Meter) metric.Int64Counter {
	c, _ := m.Int64Counter("agents.owner_offline_pages",
		metric.WithDescription("Store-owner pages for a Pi offline / back online (kind = offline|online)"))
	return c
}
