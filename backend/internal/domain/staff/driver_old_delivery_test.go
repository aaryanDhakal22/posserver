package staff

import (
	"errors"
	"testing"
)

// #27 (batch F): driver_old_delivery_minutes defaults to 60 (the owner's X),
// 0 turns it off, more than 720 is refused.
func TestDriverOldDeliveryMinutes(t *testing.T) {
	t.Parallel()
	s := DefaultSettings()
	if s.DriverOldDeliveryMinutes != 60 {
		t.Fatalf("default %d", s.DriverOldDeliveryMinutes)
	}
	for v, ok := range map[int64]bool{0: true, 60: true, 720: true, -1: false, 721: false} {
		s.DriverOldDeliveryMinutes = v
		if err := s.Validate(); (err == nil) != ok || (err != nil && !errors.Is(err, ErrBadSettings)) {
			t.Fatalf("%d: %v", v, err)
		}
	}
}
