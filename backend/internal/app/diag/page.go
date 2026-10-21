package diag

import (
	"fmt"
	"strings"
)

// Owner paging. Only the owner is paged (never the boss): the tablet tells
// staff what to do, the owner hears about what staff could not fix, a manual
// "send diagnostics", a failed setup, and "no orders while open" with nothing
// wrong locally.

// ShouldPage is the paging rule for one stored report.
func ShouldPage(r *Report) bool {
	switch r.Outcome {
	case "needs_part", "needs_owner", "not_fixed":
		return true
	}
	switch {
	case r.Kind == "manual":
		return true
	case r.Kind == "setup" && r.Outcome != "ok":
		return true
	case r.Scenario == "no_orders" && r.Outcome == "info":
		return true
	}
	return false
}

// pushoverMaxMessage is Pushover's message limit (runes).
const pushoverMaxMessage = 1024

// FormatPage builds the owner page for a report from `store` sent `via`
// ("pi" | "direct"); tabletName is the stored tablet name.
func FormatPage(store string, r *Report, via, tabletName string) (title, message string) {
	headline := r.Headline
	if headline == "" {
		headline = plain(r.Outcome)
	}
	title = clip(store+": "+headline, 250)

	var lines []string
	if r.Cause != "" {
		lines = append(lines, "Cause: "+plainCause(r.Cause))
	}
	switch {
	case r.FixedBy != "":
		lines = append(lines, "Fixed by: "+plainFix(r.FixedBy))
	case notFixed(r.Outcome):
		unit := "steps"
		if r.Steps == 1 {
			unit = "step"
		}
		lines = append(lines, fmt.Sprintf("Not fixed after %d %s", r.Steps, unit))
	}

	if tabletName == "" {
		tabletName = "unnamed"
	}
	parts := []string{"Tablet: " + tabletName}
	if r.AppVersion != "" {
		parts = append(parts, "Quibase "+r.AppVersion)
	}
	if r.AgentVersion != "" {
		parts = append(parts, "Pi "+r.AgentVersion)
	}
	sentVia := "tablet"
	if via == ViaPi {
		sentVia = "Pi"
	}
	parts = append(parts, "sent via "+sentVia)
	lines = append(lines, strings.Join(parts, " · "))

	if r.Note != "" {
		lines = append(lines, "Note: "+r.Note)
	}
	lines = append(lines, "Details: /app/admin/diagnostics?id="+r.ID.String()[:8])
	return title, clip(strings.Join(lines, "\n"), pushoverMaxMessage)
}

func notFixed(outcome string) bool {
	switch outcome {
	case "needs_part", "needs_owner", "not_fixed", "abandoned":
		return true
	}
	return false
}

// Plain words for the cause / fix codes the tablet's guides use. An unknown
// code still reads fine with its underscores turned into spaces.
var causeWords = map[string]string{
	"sd_card":        "the Pi's SD card",
	"pi_power":       "the Pi's power",
	"pi_cable":       "the Pi's network cable",
	"pi_address":     "the Pi changed address",
	"agent_down":     "the Pi's software was not running",
	"wifi":           "the tablet's Wi-Fi",
	"guest_wifi":     "the tablet was on the guest Wi-Fi",
	"router":         "the router",
	"store_internet": "the store's internet",
	"isp":            "the internet provider",
	"main_down":      "the Quicc server",
	"unlinked":       "the Pi was not linked",
	"printer_power":  "a printer's power",
	"printer_paper":  "a printer's paper or lid",
	"printer_cable":  "a printer's cable",
	"printer_switch": "the network switch",
}

var fixWords = map[string]string{
	"sd_swap":         "swapping the SD card",
	"power_cycle":     "restarting the Pi",
	"pi_unplug":       "unplugging the Pi",
	"pi_cable":        "reseating the Pi's cable",
	"router_restart":  "restarting the router",
	"wifi":            "fixing the tablet's Wi-Fi",
	"refind":          "finding the Pi again",
	"wait":            "waiting",
	"printer_restart": "restarting the printer",
	"printer_paper":   "fixing the paper or lid",
	"printer_cable":   "reseating the printer cable",
	"switch_restart":  "restarting the network switch",
	"relink":          "linking the Pi again",
}

func plainCause(code string) string {
	if w, ok := causeWords[code]; ok {
		return w
	}
	return plain(code)
}

func plainFix(code string) string {
	if w, ok := fixWords[code]; ok {
		return w
	}
	return plain(code)
}

func plain(code string) string { return strings.ReplaceAll(code, "_", " ") }
