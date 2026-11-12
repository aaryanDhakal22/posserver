// Package dbpool sets main's Postgres pool limits (HARDENING-PLAN R-4): a
// connection cap, and server-side timeouts so a stuck statement or a
// transaction left open can't hold a connection (and its locks) forever.
//
//	DB_MAX_CONNS             pool size (default 20)
//	DB_STATEMENT_TIMEOUT     Go duration, statement_timeout (default 30s; 0 = off)
//	DB_IDLE_TX_TIMEOUT       Go duration, idle_in_transaction_session_timeout (default 60s; 0 = off)
package dbpool

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultMaxConns         = 20
	DefaultStatementTimeout = 30 * time.Second
	DefaultIdleTxTimeout    = 60 * time.Second
)

// Apply sets the limits on cfg from getenv (os.Getenv in main). A bad value
// is an error (main refuses to start on it, before serving).
func Apply(cfg *pgxpool.Config, getenv func(string) string) error {
	maxConns := int32(DefaultMaxConns)
	if v := getenv("DB_MAX_CONNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return fmt.Errorf("DB_MAX_CONNS %q: want 1-1000", v)
		}
		maxConns = int32(n)
	}
	cfg.MaxConns = maxConns
	st, err := duration(getenv, "DB_STATEMENT_TIMEOUT", DefaultStatementTimeout)
	if err != nil {
		return err
	}
	idle, err := duration(getenv, "DB_IDLE_TX_TIMEOUT", DefaultIdleTxTimeout)
	if err != nil {
		return err
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(st.Milliseconds(), 10)
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(idle.Milliseconds(), 10)
	return nil
}

func duration(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q: want a Go duration like 30s (0 = off)", name, v)
	}
	return d, nil
}
