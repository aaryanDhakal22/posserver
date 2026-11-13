package menu

import (
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeRouting(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	yes, no := true, false
	cases := []struct {
		name      string
		printer   *uuid.UUID
		noKitchen *bool
		wantP     *uuid.UUID
		wantNK    *bool
	}{
		{"neither keeps both", nil, nil, nil, nil},
		{"a printer clears the flag", &id, nil, &id, &no},
		{"no printer clears the printer", &id, &yes, nil, &yes},
		{"no printer alone", nil, &yes, nil, &yes},
		{"a printer and false", &id, &no, &id, &no},
		{"category printer and false", nil, &no, nil, &no},
	}
	for _, c := range cases {
		p, nk := NormalizeRouting(c.printer, c.noKitchen)
		if (p == nil) != (c.wantP == nil) || (p != nil && *p != *c.wantP) {
			t.Errorf("%s: printer %v, want %v", c.name, p, c.wantP)
		}
		if (nk == nil) != (c.wantNK == nil) || (nk != nil && *nk != *c.wantNK) {
			t.Errorf("%s: no kitchen %v, want %v", c.name, nk, c.wantNK)
		}
	}
}
