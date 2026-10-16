// Package printerstatus caches the live printer roster each agent reports over
// the duplex channel (rpc "printer.report"). main's `printers` table stays the
// config/assignment source of truth; this cache is advisory — it lets the menu
// UI show real up/down reachability next to the configured printers.
package printerstatus

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	sseBroker "quiccpos/main/internal/infra/sse"
)

// LivePrinter mirrors the agent's printer.StatusSnapshot.
type LivePrinter struct {
	Name        string     `json:"name"`
	IP          string     `json:"ip"`
	Up          bool       `json:"up"`
	LastChecked *time.Time `json:"last_checked,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

// Report mirrors the agent's printer.PrinterReport.
type Report struct {
	ReportedAt   time.Time     `json:"reported_at"`
	AgentVersion string        `json:"agent_version"`
	Printers     []LivePrinter `json:"printers"`
}

type entry struct {
	report     Report
	receivedAt time.Time
}

// Cache holds the last report per (tenant, location).
type Cache struct {
	mu      sync.RWMutex
	entries map[sseBroker.Scope]entry
}

func NewCache() *Cache {
	return &Cache{entries: make(map[sseBroker.Scope]entry)}
}

func (c *Cache) Put(scope sseBroker.Scope, r Report) {
	c.mu.Lock()
	c.entries[scope] = entry{report: r, receivedAt: time.Now()}
	c.mu.Unlock()
}

// Get returns the cached report + how long ago it arrived. ok=false if none.
func (c *Cache) Get(scope sseBroker.Scope) (Report, time.Duration, bool) {
	c.mu.RLock()
	e, ok := c.entries[scope]
	c.mu.RUnlock()
	if !ok {
		return Report{}, 0, false
	}
	return e.report, time.Since(e.receivedAt), true
}

// Handle is the rpc.InboundHandler for agent-initiated "printer.report" pushes
// (fire-and-forget — no reply).
func (c *Cache) Handle(_ context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var r Report
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	c.Put(scope, r)
	return nil, nil
}
