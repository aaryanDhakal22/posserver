package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/trace"
)

// Wire builds main's logger plumbing around out (the stderr / console
// writer): every line still reaches out unchanged, and each event is also
// bridged into an OTLP log record with **all of its fields** as attributes
// (HARDENING-PLAN CM-0: the old hook shipped only the message, so Loki never
// saw order_id, amount_cents, staff_id …). The trace hook copies
// trace_id/span_id from the event's context (Event.Ctx(ctx) or
// logger.WithContext) onto the line, and the bridge turns them back into the
// record's trace context.
func Wire(out io.Writer, serviceName string) (io.Writer, zerolog.Hook) {
	return NewOTLPWriter(out, global.GetLoggerProvider().Logger(serviceName)), TraceHook{}
}

// TraceHook copies trace_id/span_id from the event's context onto the event.
type TraceHook struct{}

func (TraceHook) Run(e *zerolog.Event, _ zerolog.Level, _ string) {
	ctx := e.GetCtx()
	if ctx == nil {
		return
	}
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return
	}
	e.Str("trace_id", sc.TraceID().String()).
		Str("span_id", sc.SpanID().String())
}

// OTLPWriter is a zerolog.LevelWriter: it passes each event's JSON on to out
// and emits it as an OTLP log record. Body = the message; every other
// top-level key is an attribute, except level / time / message (they are
// the record's severity, timestamp and body), trace_id / span_id (the record's
// trace context) and secret-looking keys (DroppedLogKey).
type OTLPWriter struct {
	out    io.Writer
	logger otellog.Logger
}

// NewOTLPWriter bridges into logger (normally the global LoggerProvider's).
func NewOTLPWriter(out io.Writer, logger otellog.Logger) *OTLPWriter {
	return &OTLPWriter{out: out, logger: logger}
}

func (w *OTLPWriter) Write(p []byte) (int, error) {
	w.emit(zerolog.NoLevel, p)
	return w.out.Write(p)
}

func (w *OTLPWriter) WriteLevel(level zerolog.Level, p []byte) (int, error) {
	w.emit(level, p)
	if lw, ok := w.out.(zerolog.LevelWriter); ok {
		return lw.WriteLevel(level, p)
	}
	return w.out.Write(p)
}

// droppedKeys never leave the process as log attributes, whatever logged them.
var droppedKeys = []string{"pin", "password", "passwd", "secret", "token", "authorization", "cookie", "api_key", "apikey", "device_key", "session"}

// DroppedLogKey reports whether a field must not be bridged: one of
// droppedKeys, or a name ending in "_<one of them>" (manager_pin,
// session_token, …) or containing "password" / "secret".
func DroppedLogKey(k string) bool {
	k = strings.ToLower(k)
	if strings.Contains(k, "password") || strings.Contains(k, "secret") {
		return true
	}
	for _, d := range droppedKeys {
		if k == d || strings.HasSuffix(k, "_"+d) {
			return true
		}
	}
	return false
}

func (w *OTLPWriter) emit(level zerolog.Level, p []byte) {
	ctx := context.Background()
	if !w.logger.Enabled(ctx, otellog.EnabledParameters{Severity: otelSeverity(level)}) && level != zerolog.NoLevel {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil {
		return // not a JSON event (never from zerolog): stderr still has it
	}
	if level == zerolog.NoLevel {
		if s, ok := fields[zerolog.LevelFieldName].(string); ok {
			if l, err := zerolog.ParseLevel(s); err == nil {
				level = l
			}
		}
		if !w.logger.Enabled(ctx, otellog.EnabledParameters{Severity: otelSeverity(level)}) {
			return
		}
	}

	var r otellog.Record
	r.SetTimestamp(time.Now())
	r.SetSeverity(otelSeverity(level))
	r.SetSeverityText(level.String())
	if msg, ok := fields[zerolog.MessageFieldName].(string); ok {
		r.SetBody(attribute.StringValue(msg))
	}

	var tid trace.TraceID
	var sid trace.SpanID
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		v := fields[k]
		switch k {
		case zerolog.LevelFieldName, zerolog.TimestampFieldName, zerolog.MessageFieldName:
			continue
		case "trace_id":
			if s, ok := v.(string); ok {
				tid, _ = trace.TraceIDFromHex(s)
			}
			continue
		case "span_id":
			if s, ok := v.(string); ok {
				sid, _ = trace.SpanIDFromHex(s)
			}
			continue
		}
		if DroppedLogKey(k) {
			continue
		}
		attrs = append(attrs, logAttr(k, v))
	}
	r.AddAttributes(attrs...)
	if tid.IsValid() && sid.IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true,
		}))
	}
	w.logger.Emit(ctx, r)
}

// logAttr maps one decoded JSON value: strings, whole numbers (int64),
// other numbers (float64), bools; objects and arrays as their JSON text.
func logAttr(k string, v any) attribute.KeyValue {
	switch x := v.(type) {
	case string:
		return attribute.String(k, x)
	case bool:
		return attribute.Bool(k, x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return attribute.Int64(k, i)
		}
		if f, err := x.Float64(); err == nil {
			return attribute.Float64(k, f)
		}
		return attribute.String(k, x.String())
	case nil:
		return attribute.String(k, "")
	default:
		b, _ := json.Marshal(x)
		return attribute.String(k, string(b))
	}
}

func otelSeverity(level zerolog.Level) otellog.Severity {
	switch level {
	case zerolog.TraceLevel:
		return otellog.SeverityTrace
	case zerolog.DebugLevel:
		return otellog.SeverityDebug
	case zerolog.InfoLevel:
		return otellog.SeverityInfo
	case zerolog.WarnLevel:
		return otellog.SeverityWarn
	case zerolog.ErrorLevel:
		return otellog.SeverityError
	case zerolog.PanicLevel:
		return otellog.SeverityFatal1
	case zerolog.FatalLevel:
		return otellog.SeverityFatal2
	default:
		return otellog.SeverityUndefined
	}
}
