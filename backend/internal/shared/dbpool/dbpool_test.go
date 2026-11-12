package dbpool

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func cfg(t *testing.T) *pgxpool.Config {
	c, err := pgxpool.ParseConfig("host=localhost dbname=x")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDefaults(t *testing.T) {
	c := cfg(t)
	if err := Apply(c, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if c.MaxConns != 20 || c.ConnConfig.RuntimeParams["statement_timeout"] != "30000" || c.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] != "60000" {
		t.Fatalf("got max %d params %v", c.MaxConns, c.ConnConfig.RuntimeParams)
	}
}

func TestOverridesAndBadValues(t *testing.T) {
	env := map[string]string{"DB_MAX_CONNS": "35", "DB_STATEMENT_TIMEOUT": "0", "DB_IDLE_TX_TIMEOUT": "2m"}
	c := cfg(t)
	if err := Apply(c, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if c.MaxConns != 35 || c.ConnConfig.RuntimeParams["statement_timeout"] != "0" || c.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] != "120000" {
		t.Fatalf("got max %d params %v", c.MaxConns, c.ConnConfig.RuntimeParams)
	}
	for k, v := range map[string]string{"DB_MAX_CONNS": "zero", "DB_STATEMENT_TIMEOUT": "30", "DB_IDLE_TX_TIMEOUT": "-1s"} {
		if err := Apply(cfg(t), func(n string) string {
			if n == k {
				return v
			}
			return ""
		}); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
}
