package diag

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Size + field caps for a report (v1). Over-long strings are cut, never
// rejected; unknown enum values are kept (a newer Quibase may send them).
const (
	MaxBody       = 256 << 10 // whole report body
	maxHeadline   = 120
	maxNote       = 500
	maxEnum       = 40 // kind / scenario / outcome / cause / fixed_by / versions
	maxLogLines   = 300
	maxLogLine    = 500
	maxTabletID   = 100
	maxTabletName = 80
)

var (
	// ErrBadReport is a report that can't be stored (not JSON, wrong version,
	// no id / kind / outcome) — a 400.
	ErrBadReport = errors.New("bad diagnostics report")
	// ErrTooLarge is a report body over MaxBody — a 413.
	ErrTooLarge = errors.New("diagnostics report too large")
)

// Report is a parsed, size-capped report v1. The summary fields mirror the
// diag_reports columns; doc is the whole (capped) JSON that is stored.
type Report struct {
	ID           uuid.UUID
	CreatedAt    time.Time
	Kind         string
	Scenario     string // "" when the report sent null
	Outcome      string
	Headline     string
	Cause        string
	FixedBy      string
	Note         string
	AppVersion   string // tablet.app_version
	AgentVersion string // pi.version
	TabletName   string // tablet.name
	Steps        int

	doc map[string]any
}

// ParseReport decodes + validates + caps a report v1 body.
func ParseReport(raw []byte) (*Report, error) {
	if len(raw) > MaxBody {
		return nil, ErrTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numbers (android_sdk, alarm_volume_pct, …) exact
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, fmt.Errorf("%w: not a JSON object", ErrBadReport)
	}
	if v, _ := doc["v"].(json.Number); v.String() != "1" {
		return nil, fmt.Errorf("%w: unsupported version (want v: 1)", ErrBadReport)
	}
	r := &Report{doc: doc}

	idStr, _ := doc["id"].(string)
	id, err := uuid.Parse(idStr)
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("%w: id must be a uuid", ErrBadReport)
	}
	r.ID = id
	doc["id"] = id.String()

	createdStr, _ := doc["created_at"].(string)
	if t, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
		r.CreatedAt = t
	} else {
		r.CreatedAt = time.Now().UTC()
		doc["created_at"] = r.CreatedAt.Format(time.RFC3339)
	}

	r.Kind = capField(doc, "kind", maxEnum)
	r.Outcome = capField(doc, "outcome", maxEnum)
	if r.Kind == "" || r.Outcome == "" {
		return nil, fmt.Errorf("%w: kind and outcome are required", ErrBadReport)
	}
	r.Scenario = capField(doc, "scenario", maxEnum)
	r.Headline = capField(doc, "headline", maxHeadline)
	r.Cause = capField(doc, "cause", maxEnum)
	r.FixedBy = capField(doc, "fixed_by", maxEnum)
	r.Note = capField(doc, "note", maxNote)
	if steps, ok := doc["steps"].([]any); ok {
		r.Steps = len(steps)
	}
	if lines, ok := doc["log"].([]any); ok {
		if len(lines) > maxLogLines { // keep the newest (the tablet's log is oldest-first)
			lines = lines[len(lines)-maxLogLines:]
		}
		for i, l := range lines {
			s, ok := l.(string)
			if !ok {
				s = fmt.Sprint(l)
			}
			lines[i] = clip(s, maxLogLine)
		}
		doc["log"] = lines
	}
	if tab, ok := doc["tablet"].(map[string]any); ok {
		r.TabletName = capField(tab, "name", maxTabletName)
		r.AppVersion = capField(tab, "app_version", maxEnum)
	}
	if pi, ok := doc["pi"].(map[string]any); ok {
		r.AgentVersion = capField(pi, "version", maxEnum)
	}
	return r, nil
}

// MergePi folds what the Pi adds when it relays a report (its version, a
// health snapshot) into the report's `pi` object; the Pi's values win.
func (r *Report) MergePi(pi map[string]any) {
	if len(pi) == 0 {
		return
	}
	cur, _ := r.doc["pi"].(map[string]any)
	if cur == nil {
		cur = map[string]any{}
	}
	for k, v := range pi {
		cur[k] = v
	}
	r.doc["pi"] = cur
	r.AgentVersion = capField(cur, "version", maxEnum)
}

// JSON is the capped report as stored in diag_reports.report.
func (r *Report) JSON() ([]byte, error) { return json.Marshal(r.doc) }

// capField reads m[key] as a trimmed string capped at n runes and writes the
// capped value back (null / absent / non-string reads as "" and is left as is).
func capField(m map[string]any, key string, n int) string {
	s, ok := m[key].(string)
	if !ok {
		return ""
	}
	s = clip(strings.TrimSpace(s), n)
	m[key] = s
	return s
}

// clip cuts s to at most n runes.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
