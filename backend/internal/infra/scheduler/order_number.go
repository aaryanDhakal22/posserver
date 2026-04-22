package scheduler

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// resetHours maps each weekday to the local hour at which the order number
// counter resets back to 1100:
//
//	Mon–Thu → 01:00
//	Fri–Sat → 02:00
//	Sun     → 00:00 (midnight)
var resetHours = map[time.Weekday]int{
	time.Sunday:    0,
	time.Monday:    1,
	time.Tuesday:   1,
	time.Wednesday: 1,
	time.Thursday:  1,
	time.Friday:    2,
	time.Saturday:  2,
}

// nextResetTime returns the next scheduled reset moment after now.
// Times are evaluated in now's location so that TZ env var controls the clock.
func nextResetTime(now time.Time) time.Time {
	loc := now.Location()

	// Today's reset wall-clock time
	y, m, d := now.Date()
	todayReset := time.Date(y, m, d, resetHours[now.Weekday()], 0, 0, 0, loc)

	if now.Before(todayReset) {
		return todayReset
	}

	// Already past today's reset — compute tomorrow's
	tomorrow := now.AddDate(0, 0, 1)
	ty, tm, td := tomorrow.Date()
	return time.Date(ty, tm, td, resetHours[tomorrow.Weekday()], 0, 0, 0, loc)
}

// StartOrderNumberReset runs a background goroutine that resets the order
// number counter on the configured schedule. resetFn is called once per cycle.
// Blocks until ctx is cancelled.
func StartOrderNumberReset(ctx context.Context, resetFn func(context.Context) error, logger zerolog.Logger) {
	log := logger.With().Str("module", "order-number-scheduler").Logger()

	go func() {
		for {
			next := nextResetTime(time.Now())
			log.Info().Time("next_reset", next).Msg("order number reset scheduled")

			select {
			case <-time.After(time.Until(next)):
				if err := resetFn(ctx); err != nil {
					log.Error().Err(err).Msg("failed to reset order number counter")
				} else {
					log.Info().Msg("order number counter reset to 1100")
				}
			case <-ctx.Done():
				log.Info().Msg("order number scheduler stopped")
				return
			}
		}
	}()
}
