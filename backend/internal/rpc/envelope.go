// Package rpc is main's half of the standardized duplex agent↔main channel.
//
// The agent lives on the restaurant LAN behind NAT, so it always DIALS main: it
// holds a long-lived SSE stream (main→agent, "down") and makes authenticated
// HTTP POSTs (agent→main, "up"). This package layers a small typed RPC on top of
// those two half-channels:
//
//   - main→agent COMMANDS ride the existing SSE as `event: command` frames.
//   - agent→main REPLIES + REPORTS are POSTed to one endpoint, /store/v1/agent/rpc.
//   - A correlation id ties a reply back to its request; unsolicited reports use
//     a fresh id and a registered inbound handler.
//
// New message types just register — no new endpoints. The agent mirrors this in
// `agent/internal/rpc` with a byte-identical Envelope (pinned in contract/agent.md).
package rpc

import "errors"

import "encoding/json"

// Message type constants. Requests and their replies share the correlation id;
// a reply's Type is the request Type + ".reply" (Deliver matches on id, so the
// exact reply Type is not load-bearing).
const (
	MsgPing                 = "ping"
	MsgPrinterReport        = "printer.report"         // agent → main, unsolicited push
	MsgPrinterReportRequest = "printer.report.request" // main → agent, pull

	// Pi updates (2.0.6). An agent older than 2.0.6 doesn't answer these
	// commands — the request times out and main records the update as
	// "unreachable".
	MsgAgentUpdate       = "agent.update"        // main → agent {id, force, requested_by}
	MsgAgentRollback     = "agent.rollback"      // main → agent {id, requested_by}
	MsgAgentUpdateStatus = "agent.update.status" // agent → main, every state change

	// Printer auto-follow + found printers (2.0.6), agent → main requests.
	MsgPrinterMoved = "printer.moved" // {printer_id, old_ip, new_ip, mac} → {applied, reason}
	MsgPrinterAdd   = "printer.add"   // {name, ip, mac} → {ok, id, name, ip} | {ok:false, code}
)

// Envelope is the wire shape for every duplex message, both directions. It must
// stay byte-identical to agent/internal/rpc.Envelope.
// ErrBadPayload marks an inbound report whose payload can't be decoded — the
// agent gets a 400 (a client error it won't fix by retrying the same bytes)
// instead of a 500.
var ErrBadPayload = errors.New("bad rpc payload")

type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
