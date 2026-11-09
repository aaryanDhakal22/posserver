package customers

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"

	"github.com/rs/zerolog"
)

// FakeCodes stands in for Twilio Verify on dev / staging: the code is logged
// (and readable by a dev-only admin route for tests) instead of texted.
type FakeCodes struct {
	mu     sync.Mutex
	codes  map[string]string // E.164 → the current code
	sent   map[string][]string
	logger zerolog.Logger
}

func NewFakeCodes(lg zerolog.Logger) *FakeCodes {
	return &FakeCodes{codes: map[string]string{}, sent: map[string][]string{}, logger: lg.With().Str("module", "fake-codes").Logger()}
}

func (f *FakeCodes) Send(_ context.Context, to, channel string) error {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	f.mu.Lock()
	f.codes[to] = code
	f.sent[to] = append(f.sent[to], channel)
	f.mu.Unlock()
	f.logger.Info().Str("to", to).Str("channel", channel).Str("code", code).Msg("FAKE sign-in code (not sent)")
	return nil
}

func (f *FakeCodes) Check(_ context.Context, to, code string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.codes[to]; ok && c == code {
		delete(f.codes, to) // a code works once
		return true, nil
	}
	return false, nil
}

// Last is the code waiting for a number and the channels used so far (tests).
func (f *FakeCodes) Last(to string) (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.codes[to], append([]string(nil), f.sent[to]...)
}
