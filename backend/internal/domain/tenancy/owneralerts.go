package tenancy

import (
	"encoding/json"
	"time"
)

// Owner alerts (agent 2.5.2, contract/agent.md §2.1e): the store owner gets
// only a few plain-English alerts, tuned by these location config keys. The
// Pi reads them from store_config; main 4.0.2 reads the ones its own owner
// page for a Pi offline needs (same names, defaults and clamps), so one
// setting in the Store pages drives both.
const (
	OwnerAlertsKey                = "owner_alerts"                 // bool, default true
	OwnerAlertOfflineMinutesKey   = "owner_alert_offline_minutes"  // 5, 1-120
	OwnerAlertCooldownMinutesKey  = "owner_alert_cooldown_minutes" // 30, 30-1440
	OwnerAlertMaxPerHourKey       = "owner_alert_max_per_hour"     // 6, 1-60
	OwnerAlertMaxPerDayKey        = "owner_alert_max_per_day"      // 20, 1-200
	ownerAlertMinCooldownMinutes  = 30
	ownerAlertDefaultOfflineMins  = 5
	ownerAlertDefaultCooldownMins = 30
)

// OwnerAlertSettings are the thresholds main's owner offline page uses.
type OwnerAlertSettings struct {
	Enabled bool
	// Offline: the Pi gone this long is an outage the owner hears about.
	Offline time.Duration
	// Cooldown: the same message (offline / back online) again only after this.
	Cooldown   time.Duration
	MaxPerHour int
	MaxPerDay  int
}

// OwnerAlertDefaults are the agent's defaults (the owner, 2026-11-12).
func OwnerAlertDefaults() OwnerAlertSettings {
	return OwnerAlertSettings{
		Enabled:    true,
		Offline:    ownerAlertDefaultOfflineMins * time.Minute,
		Cooldown:   ownerAlertDefaultCooldownMins * time.Minute,
		MaxPerHour: 6,
		MaxPerDay:  20,
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// configInt reads a number key (JSON numbers decode as float64); anything
// else is the fallback, as on the Pi.
func configInt(cfg Config, key string, fallback int) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}

// OwnerAlertSettingsFrom reads the thresholds from a location config (the
// defaults for a missing key; each clamped like the agent does).
func OwnerAlertSettingsFrom(cfg Config) OwnerAlertSettings {
	s := OwnerAlertDefaults()
	if v, ok := cfg[OwnerAlertsKey].(bool); ok {
		s.Enabled = v
	}
	s.Offline = time.Duration(clampInt(configInt(cfg, OwnerAlertOfflineMinutesKey, ownerAlertDefaultOfflineMins), 1, 120)) * time.Minute
	s.Cooldown = time.Duration(clampInt(configInt(cfg, OwnerAlertCooldownMinutesKey, ownerAlertDefaultCooldownMins),
		ownerAlertMinCooldownMinutes, 1440)) * time.Minute
	s.MaxPerHour = clampInt(configInt(cfg, OwnerAlertMaxPerHourKey, s.MaxPerHour), 1, 60)
	s.MaxPerDay = clampInt(configInt(cfg, OwnerAlertMaxPerDayKey, s.MaxPerDay), 1, 200)
	return s
}

// ownerAlertFallbackHours: when a store has no (or invalid) opening hours,
// owner alerts go out 09:00-02:00 every day (the agent's rule).
var ownerAlertFallbackHours = func() OpeningHours {
	h := OpeningHours{}
	for _, d := range Weekdays {
		h[d] = []HoursRange{{Open: "09:00", Close: "02:00"}}
	}
	return h
}()

// OwnerAlertsOpen reports whether an owner alert may go out at `at`: inside
// the location's opening hours (no grace), else inside 09:00-02:00.
func OwnerAlertsOpen(cfg Config, at time.Time, loc *time.Location) bool {
	if h, ok, err := HoursFromConfig(cfg); err == nil && ok {
		return h.IsOpen(at, loc, 0)
	}
	return ownerAlertFallbackHours.IsOpen(at, loc, 0)
}
