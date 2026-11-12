package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"strings"
)

// Command is `app migrate [status]` (deploy/main-deploy.sh): the
// image's migrations run in a one-off container and the process exits, so a
// release migrates once, as the migrator role, before any api slot starts.
//
//	app migrate          apply every pending migration, print the status, exit 0
//	app migrate status   print {"current","latest","pending"} as JSON, change nothing
//
// The database comes from DB_HOST / DB_PORT / DB_NAME and, when set,
// DB_MIGRATOR_USER / DB_MIGRATOR_PASSWORD (the schema owner), else DB_USER /
// DB_PASSWORD. Nothing else is read: no SQS, no HTTP server, no workers.
// Exit codes: 0 ok, 1 failed, 2 usage.
func Command(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	user, pass := os.Getenv("DB_MIGRATOR_USER"), os.Getenv("DB_MIGRATOR_PASSWORD")
	if user == "" {
		user, pass = os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD")
	}
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "5432"
	}
	if os.Getenv("DB_HOST") == "" || os.Getenv("DB_NAME") == "" || user == "" {
		fmt.Fprintln(os.Stderr, "migrate: DB_HOST, DB_NAME and DB_MIGRATOR_USER or DB_USER are required")
		return 2
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		quoteDSN(os.Getenv("DB_HOST")), quoteDSN(port), quoteDSN(user), quoteDSN(pass), quoteDSN(os.Getenv("DB_NAME")))

	mode := ""
	if len(args) > 0 {
		mode = args[0]
	}
	switch mode {
	case "", "up":
		before, err := ReadStatus(ctx, dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "migrate: status:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "migrate: %s as %s: version %d, %d pending (latest %d)\n",
			os.Getenv("DB_NAME"), user, before.Current, before.Pending, before.Latest)
		if err := Run(ctx, dsn); err != nil {
			fmt.Fprintln(os.Stderr, "migrate: failed:", err)
			return 1
		}
		after, err := ReadStatus(ctx, dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "migrate: status after:", err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"before": before, "after": after})
		if after.Pending != 0 {
			fmt.Fprintf(os.Stderr, "migrate: %d still pending after the run\n", after.Pending)
			return 1
		}
		return 0
	case "status":
		st, err := ReadStatus(ctx, dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "migrate: status:", err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(st)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "usage: app migrate [status]  (unknown %q)\n", mode)
		return 2
	}
}

// quoteDSN quotes a keyword/value DSN value (cmd/api's dsnValue).
func quoteDSN(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}
