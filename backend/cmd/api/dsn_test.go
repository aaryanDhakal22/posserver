package main

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDSNValueSurvivesAwkwardPasswords(t *testing.T) {
	for _, pw := range []string{"plain", "has space", `it's`, `back\slash`, "a=b c='d'"} {
		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			dsnValue("db"), dsnValue("5432"), dsnValue("quicc"), dsnValue(pw), dsnValue("quicc_v2"))
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("password %q: %v", pw, err)
		}
		if cfg.ConnConfig.Password != pw || cfg.ConnConfig.Database != "quicc_v2" || cfg.ConnConfig.Host != "db" {
			t.Errorf("password %q parsed as %q (db %q, host %q)", pw, cfg.ConnConfig.Password, cfg.ConnConfig.Database, cfg.ConnConfig.Host)
		}
	}
}
