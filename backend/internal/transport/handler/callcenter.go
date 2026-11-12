package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	callcenterApp "quiccpos/main/internal/app/callcenter"
	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/staff"
	"quiccpos/main/internal/infra/database/auditconn"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// CallcenterHandler serves the call center's remote terminal
// (/callcenter/v1/*, contract/main.md §2.5zh) and its Store pages' routes
// (devices, the per-agent report).
//
// The call-center web app's server calls these (the device token rides as
// Authorization: Bearer qccd_…, kept by that server in an httpOnly cookie):
// main sets no CORS headers and no cookies here.
type CallcenterHandler struct {
	svc     *callcenterApp.Service
	tenancy *tenancyApp.Service
	audit   appMiddleware.AuditWriter
	logins  *loginLimiter
	logger  zerolog.Logger
}

func NewCallcenterHandler(svc *callcenterApp.Service, tenancy *tenancyApp.Service, audit appMiddleware.AuditWriter, lg zerolog.Logger) *CallcenterHandler {
	return &CallcenterHandler{svc: svc, tenancy: tenancy, audit: audit, logins: newLoginLimiter(),
		logger: lg.With().Str("module", "callcenter-handler").Logger()}
}

// clip cuts s to at most n bytes (what a caller typed never bloats a log row).
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func ccErr(c *echo.Context, status int, code, msg string) error {
	return c.JSON(status, errCode(msg, code))
}

// note writes an app audit row (best effort).
func (h *CallcenterHandler) note(c *echo.Context, d *callcenterApp.Device, action string, status int, params map[string]string) {
	if h.audit == nil {
		return
	}
	ctx := c.Request().Context()
	actor, _ := appMiddleware.AuditActor(ctx)
	if actor.Kind == "" {
		actor.Kind = "callcenter"
		actor.IP = c.RealIP()
	}
	actor.RequestID = clip(actor.RequestID, 64) // a caller's X-Request-ID never bloats the audit log
	entity := ""
	if d != nil {
		actor.ID, actor.TenantID, actor.LocationID = d.ID.String(), d.TenantID.String(), d.LocationID.String()
		if actor.Name == "" {
			actor.Name = d.Name
		}
		entity = d.ID.String()
	}
	_, traceID := auditconn.Settings(ctx, func(context.Context) (auditconn.Actor, bool) { return actor, true })
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := h.audit.WriteApp(wctx, appMiddleware.AuditEntry{Actor: actor, Method: action, Route: "callcenter", EntityID: entity,
		Params: params, Status: status, TraceID: traceID, At: time.Now()}); err != nil {
		h.logger.Warn().Err(err).Str("action", action).Msg("audit: callcenter row not written")
	}
}

// device authenticates the request's device token and puts the actor on the
// request's context. On failure it has answered (401 device_required).
func (h *CallcenterHandler) device(c *echo.Context) (callcenterApp.Device, bool, error) {
	tok := strings.TrimSpace(strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer "))
	d, err := h.svc.Authenticate(c.Request().Context(), tok)
	if errors.Is(err, callcenterApp.ErrDeviceRequired) {
		return d, false, ccErr(c, http.StatusUnauthorized, "device_required", "Sign in with the call-center login.")
	}
	if err != nil {
		h.logger.Error().Err(err).Msg("callcenter auth")
		return d, false, ccErr(c, http.StatusInternalServerError, "internal", "sign-in check failed")
	}
	a := appMiddleware.CallcenterActor{DeviceID: d.ID.String(), DeviceName: d.Name, TenantID: d.TenantID.String(), LocationID: d.LocationID.String()}
	if d.StaffID != nil {
		a.StaffID = d.StaffID.String()
	}
	c.SetRequest(c.Request().WithContext(appMiddleware.WithCallcenter(c.Request().Context(), a)))
	return d, true, nil
}

func (h *CallcenterHandler) fail(c *echo.Context, err error, what string) error {
	h.logger.Error().Err(err).Msg(what)
	return ccErr(c, http.StatusInternalServerError, "internal", what+" failed")
}

// --- store login → device token ---------------------------------------------------

// POST /callcenter/v1/login {username, password, device_name?}
func (h *CallcenterHandler) Login(c *echo.Context) error {
	ctx := c.Request().Context()
	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		DeviceName string `json:"device_name"`
	}
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Username) == "" || req.Password == "" {
		return ccErr(c, http.StatusBadRequest, "bad_request", "username and password are required")
	}
	// The call center's OWN shared login (Store › Staff sets it), with its own
	// limiter: the store manager's login is refused here (it opens Store pages
	// and links a Pi), and failures here never lock the manager out.
	if stop, err := h.logins.throttledCode(c, req.Username); stop {
		return err
	}
	tid, lid, err := h.svc.CheckLogin(ctx, req.Username, req.Password)
	if err != nil {
		if errors.Is(err, callcenterApp.ErrInvalidCredentials) {
			h.logins.failedFrom(c, req.Username)
			h.note(c, nil, "callcenter.login.failed", http.StatusUnauthorized, map[string]string{"username": clip(strings.TrimSpace(req.Username), 64)})
			return ccErr(c, http.StatusUnauthorized, "invalid_credentials", "Wrong call-center login or password.")
		}
		return h.fail(c, err, "callcenter login")
	}
	h.logins.succeeded(req.Username)
	tok, d, err := h.svc.NewDevice(ctx, tid, lid, req.DeviceName)
	if errors.Is(err, callcenterApp.ErrTooMany) {
		return ccErr(c, http.StatusConflict, "too_many", "Too many call-center devices: remove one in Store › Staff.")
	}
	if err != nil {
		return h.fail(c, err, "callcenter login")
	}
	h.note(c, &d, "callcenter.login", http.StatusOK, map[string]string{"device_name": d.Name})
	st, err := h.svc.StoreState(ctx, d)
	if err != nil {
		return h.fail(c, err, "callcenter login")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"device_token": tok,
		"device":       map[string]any{"id": d.ID, "name": d.Name, "created_at": d.CreatedAt},
		"store":        st.Store,
	})
}

// POST /callcenter/v1/logout — forget this browser.
func (h *CallcenterHandler) Logout(c *echo.Context) error {
	d, ok, err := h.device(c)
	if !ok {
		return err
	}
	if _, err := h.svc.Revoke(c.Request().Context(), d.TenantID, d.LocationID, d.ID); err != nil {
		return h.fail(c, err, "callcenter logout")
	}
	h.note(c, &d, "callcenter.logout", http.StatusNoContent, nil)
	return c.NoContent(http.StatusNoContent)
}

func (h *CallcenterHandler) sessionBody(ctx context.Context, d callcenterApp.Device) (map[string]any, error) {
	st, err := h.svc.Settings(ctx, d.LocationID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"staff": nil, "settings": settingsBody(st)}
	p, ok, err := h.svc.Agent(ctx, d)
	if err != nil {
		return nil, err
	}
	if ok {
		out["staff"] = callcenterStaff(p)
		out["role"] = staff.RoleCallcenter
		out["limits"] = map[string]any{"refund_cents": 0, "discount_pct": 0}
		out["expires_at"] = d.SessionExpires.UTC()
	}
	return out, nil
}

func callcenterStaff(p staff.Person) map[string]any {
	return map[string]any{"id": p.ID, "first_name": p.FirstName, "last_name": p.LastName, "name": callcenterApp.DisplayName(p), "role": p.Role}
}

// settingsBody is the store's terminal settings plus idle_lock_minutes (the
// call center's own name for callcenter_idle_lock_minutes).
func settingsBody(st staff.Settings) map[string]any {
	out := map[string]any{}
	if b, err := json.Marshal(st); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	out["idle_lock_minutes"] = st.CallcenterIdleLockMinutes
	return out
}

// GET /callcenter/v1/me
func (h *CallcenterHandler) Me(c *echo.Context) error {
	d, ok, err := h.device(c)
	if !ok {
		return err
	}
	ctx := c.Request().Context()
	st, err := h.svc.StoreState(ctx, d)
	if err != nil {
		return h.fail(c, err, "callcenter me")
	}
	sess, err := h.sessionBody(ctx, d)
	if err != nil {
		return h.fail(c, err, "callcenter me")
	}
	var session any
	if sess["staff"] != nil {
		session = map[string]any{"staff": sess["staff"], "role": sess["role"], "expires_at": sess["expires_at"]}
	}
	settings, _ := sess["settings"].(map[string]any)
	return c.JSON(http.StatusOK, map[string]any{
		"store": st.Store, "open": st.Open, "closed_reason": st.ClosedReason, "closed_note": "",
		"next_open_at": st.NextOpenAt, "closes_at": st.ClosesAt, "paused": st.Paused, "pause_note": st.PauseNote,
		"hours":    st.Hours,
		"pi":       map[string]any{"online": st.PiOnline},
		"settings": map[string]any{"idle_lock_minutes": settings["idle_lock_minutes"]},
		"square":   h.svc.Square(ctx, d.LocationID),
		"device":   map[string]any{"id": d.ID, "name": d.Name},
		"session":  session,
	})
}

// --- the agent's PIN session ---------------------------------------------------------

// POST /callcenter/v1/session {pin}
func (h *CallcenterHandler) SessionStart(c *echo.Context) error {
	d, ok, err := h.device(c)
	if !ok {
		return err
	}
	ctx := c.Request().Context()
	var req struct {
		Pin string `json:"pin"`
	}
	if err := c.Bind(&req); err != nil {
		return ccErr(c, http.StatusBadRequest, "bad_pin", "The PIN is 4 digits.")
	}
	p, expires, err := h.svc.SignIn(ctx, d, req.Pin)
	var th callcenterApp.Throttled
	switch {
	case errors.As(err, &th):
		c.Response().Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(th.RetryAfter.Seconds()))))
		return ccErr(c, http.StatusTooManyRequests, "too_many_attempts", "Too many wrong PINs: wait a moment.")
	case errors.Is(err, callcenterApp.ErrBadPin):
		return ccErr(c, http.StatusBadRequest, "bad_pin", "The PIN is 4 digits.")
	case errors.Is(err, callcenterApp.ErrWrongPin):
		h.note(c, &d, "callcenter.pin.failed", http.StatusUnauthorized, nil)
		return ccErr(c, http.StatusUnauthorized, "wrong_pin", "Wrong PIN.")
	case errors.Is(err, callcenterApp.ErrWrongRole):
		// Answered exactly like a wrong PIN (the pad must not tell which store
		// PINs exist); only the audit row says what it was.
		h.note(c, &d, "callcenter.pin.failed", http.StatusUnauthorized, map[string]string{"reason": "wrong_role"})
		return ccErr(c, http.StatusUnauthorized, "wrong_pin", "Wrong PIN.")
	case errors.Is(err, callcenterApp.ErrDeviceRequired):
		return ccErr(c, http.StatusUnauthorized, "device_required", "Sign in with the call-center login.")
	case errors.Is(err, callcenterApp.ErrDeviceRevoked):
		h.note(c, &d, "callcenter.device.revoked", http.StatusUnauthorized, map[string]string{"reason": "pin_guessing"})
		return ccErr(c, http.StatusUnauthorized, "device_required", "Too many wrong PINs: sign in with the call-center login again.")
	case err != nil:
		return h.fail(c, err, "callcenter sign-in")
	}
	h.note(c, &d, "callcenter.session.start", http.StatusOK, map[string]string{"staff_id": p.ID.String(), "staff_name": callcenterApp.DisplayName(p)})
	st, err := h.svc.Settings(ctx, d.LocationID)
	if err != nil {
		return h.fail(c, err, "callcenter sign-in")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"staff": callcenterStaff(p), "role": staff.RoleCallcenter,
		"limits":   map[string]any{"refund_cents": 0, "discount_pct": 0},
		"settings": settingsBody(st), "expires_at": expires.UTC(),
	})
}

// GET /callcenter/v1/session — never 401 for a missing session.
func (h *CallcenterHandler) SessionGet(c *echo.Context) error {
	d, ok, err := h.device(c)
	if !ok {
		return err
	}
	body, err := h.sessionBody(c.Request().Context(), d)
	if err != nil {
		return h.fail(c, err, "callcenter session")
	}
	return c.JSON(http.StatusOK, body)
}

// agent is the device's signed-in agent; on none it has answered 401 login_required.
func (h *CallcenterHandler) agent(c *echo.Context, d callcenterApp.Device) (staff.Person, bool, error) {
	p, ok, err := h.svc.Agent(c.Request().Context(), d)
	if err != nil {
		return p, false, h.fail(c, err, "callcenter session")
	}
	if !ok {
		return p, false, ccErr(c, http.StatusUnauthorized, "login_required", "Enter your PIN.")
	}
	return p, true, nil
}

// PUT /callcenter/v1/session — keep-alive.
func (h *CallcenterHandler) SessionTouch(c *echo.Context) error {
	d, ok, err := h.device(c)
	if !ok {
		return err
	}
	if _, ok, err := h.agent(c, d); !ok {
		return err
	}
	h.svc.Active(c.Request().Context(), d)
	return c.NoContent(http.StatusNoContent)
}

// DELETE /callcenter/v1/session — lock.
func (h *CallcenterHandler) SessionEnd(c *echo.Context) error {
	d, ok, err := h.device(c)
	if !ok {
		return err
	}
	if d.StaffID != nil {
		h.svc.Lock(c.Request().Context(), d)
		h.note(c, &d, "callcenter.session.end", http.StatusNoContent, map[string]string{"reason": "locked", "staff_id": d.StaffID.String()})
	}
	return c.NoContent(http.StatusNoContent)
}

// --- the relay ------------------------------------------------------------------------

func (h *CallcenterHandler) offline(c *echo.Context) error {
	return ccErr(c, http.StatusServiceUnavailable, callcenterApp.CodeStoreOffline,
		"The store's system is offline. Nothing was sent: call the store.")
}

// ANY /callcenter/v1/pi/* — the Pi's /api/terminal/<path> for the allowlist.
func (h *CallcenterHandler) Relay(c *echo.Context) error {
	d, ok, err := h.device(c)
	if !ok {
		return err
	}
	p, ok, err := h.agent(c, d)
	if !ok {
		return err
	}
	ctx := c.Request().Context()
	req := c.Request()
	method := req.Method
	path, clean := callcenterApp.CleanPath(c.Param("*"))
	refused := func(status int, code, what, msg string) error {
		h.note(c, &d, "callcenter.refused", status, map[string]string{"method": method, "path": path, "code": code, "what": what,
			"staff_id": p.ID.String()})
		body := map[string]any{"error": msg, "code": code}
		if what != "" {
			body["what"] = what
		}
		return c.JSON(status, body)
	}
	if !clean || !callcenterApp.Allowed(method, path) {
		return refused(http.StatusForbidden, callcenterApp.CodeNotAllowed, "route", "The call center can't do that.")
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), req.Body, callcenterApp.MaxBody))
	if err != nil {
		return ccErr(c, http.StatusRequestEntityTooLarge, "too_large", "The request is too large.")
	}
	if len(body) > 0 && !json.Valid(body) {
		return ccErr(c, http.StatusBadRequest, "bad_request", "The body must be JSON.")
	}
	if r := callcenterApp.Guard(method, path, body); r != nil {
		return refused(r.Status, r.Code, r.What, r.Error)
	}
	h.svc.Active(ctx, d) // the agent is at work: the session stays alive
	if callcenterApp.NeedsOpenStore(method, path) {
		st, err := h.svc.StoreState(ctx, d)
		if err != nil {
			return h.fail(c, err, "callcenter relay")
		}
		if !st.Open {
			h.note(c, &d, "callcenter.refused", http.StatusConflict, map[string]string{"method": method, "path": path, "code": callcenterApp.CodeStoreClosed})
			return c.JSON(http.StatusConflict, map[string]any{"error": "The store is closed: no orders now.", "code": callcenterApp.CodeStoreClosed,
				"open": false, "next_open_at": st.NextOpenAt})
		}
	}
	headers := map[string]string{}
	if v := req.Header.Get("If-None-Match"); v != "" {
		headers["If-None-Match"] = v
	}
	rep, err := h.svc.Relay(ctx, d, p.ID, method, path, req.URL.RawQuery, headers, body)
	if errors.Is(err, callcenterApp.ErrOffline) {
		return h.offline(c)
	}
	if errors.Is(err, callcenterApp.ErrBusy) {
		// The Pi is there, its relay is full for a moment: nothing was done; try again.
		c.Response().Header().Set("Retry-After", "1")
		return ccErr(c, http.StatusServiceUnavailable, callcenterApp.CodeStoreBusy, "The store's system is busy for a moment. Nothing was sent: try again.")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil // the browser left
		}
		return h.fail(c, err, "callcenter relay")
	}
	w := c.Response()
	for _, k := range []string{"ETag", "Retry-After"} {
		if v := rep.Headers[k]; v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if rep.Status == http.StatusNoContent || rep.Status == http.StatusNotModified || len(rep.Body) == 0 || string(rep.Body) == "null" {
		return c.NoContent(rep.Status)
	}
	return c.JSONBlob(rep.Status, rep.Body)
}

// --- events ---------------------------------------------------------------------------

// GET /callcenter/v1/events — the Pi's terminal events for this store, under
// their own names, plus main's hello / store / session.ended.
func (h *CallcenterHandler) Events(c *echo.Context) error {
	d, ok, err := h.device(c)
	if !ok {
		return err
	}
	ctx := c.Request().Context()
	sub, cancel := h.svc.Subscribe(ctx, d)
	defer cancel()

	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return echo.ErrInternalServerError
	}
	type storeState struct {
		PiOnline     bool   `json:"pi_online"`
		Open         bool   `json:"open"`
		ClosedReason string `json:"closed_reason"`
	}
	state := func() storeState {
		st, err := h.svc.StoreState(ctx, d)
		if err != nil {
			return storeState{PiOnline: h.svc.PiOnline(d.Scope())}
		}
		return storeState{PiOnline: st.PiOnline, Open: st.Open, ClosedReason: st.ClosedReason}
	}
	write := func(name string, data any) {
		b, ok := data.(json.RawMessage)
		if !ok {
			b, _ = json.Marshal(data)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		flusher.Flush()
	}
	last := state()
	fmt.Fprint(w, "retry: 3000\n\n")
	write("hello", last)

	// The Pi's events are an agent's to see: only while someone is signed in on
	// this browser (security review M6). A locked screen keeps its stream but
	// gets main's own hello / store / session.ended only.
	signedIn := d.StaffID != nil
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	poll := time.NewTicker(3 * time.Second) // the Pi's link + open / closed, this device's session
	defer poll.Stop()
	tok := strings.TrimSpace(strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer "))
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-sub.C:
			switch {
			case ev.Name == "session.started":
				signedIn = true // main's own note: nothing to send
			case ev.Name == "session.ended":
				signedIn = false
				write(ev.Name, ev.Data)
			case signedIn:
				write(ev.Name, ev.Data)
			}
			if ev.Close {
				return nil
			}
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-poll.C:
			now := state()
			if now != last {
				if now.PiOnline && !last.PiOnline {
					h.svc.Want(ctx, d.Scope(), true) // the Pi is back: ask for its events again
				}
				last = now
				write("store", now)
			}
			h.svc.Want(ctx, d.Scope(), false)
			// The token may have been revoked, the session ended by idling.
			cur, err := h.svc.Authenticate(ctx, tok)
			if errors.Is(err, callcenterApp.ErrDeviceRequired) {
				write("session.ended", map[string]string{"reason": "device_revoked"})
				return nil
			}
			if err == nil {
				signedIn = cur.StaffID != nil
			}
		}
	}
}

// --- Store pages --------------------------------------------------------------------

// GET /store/v1/me/callcenter/devices
func (h *CallcenterHandler) MeDevices(c *echo.Context) error {
	_, lid, _ := meScope(c)
	out, err := h.svc.Devices(c.Request().Context(), lid)
	if err != nil {
		return h.fail(c, err, "callcenter devices")
	}
	return c.JSON(http.StatusOK, out)
}

// DELETE /store/v1/me/callcenter/devices/:id
func (h *CallcenterHandler) MeRevokeDevice(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return ccErr(c, http.StatusBadRequest, "bad_request", "bad id")
	}
	ok, err := h.svc.Revoke(c.Request().Context(), tid, lid, id)
	if err != nil {
		return h.fail(c, err, "callcenter devices")
	}
	if !ok {
		return ccErr(c, http.StatusNotFound, "not_found", "No such device.")
	}
	return c.NoContent(http.StatusNoContent)
}

// GET /store/v1/me/callcenter/login — the call center's shared login (never the password).
func (h *CallcenterHandler) MeLogin(c *echo.Context) error {
	_, lid, _ := meScope(c)
	v, err := h.svc.LoginInfo(c.Request().Context(), lid)
	if err != nil {
		return h.fail(c, err, "callcenter login")
	}
	return c.JSON(http.StatusOK, v)
}

// PUT /store/v1/me/callcenter/login {username, password, keep_devices?} — set
// the call center's own login. It must not be the store manager's login: not
// the same username, not the same password.
func (h *CallcenterHandler) MeSetLogin(c *echo.Context) error {
	ctx := c.Request().Context()
	tid, lid, _ := meScope(c)
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		KeepDevices bool   `json:"keep_devices"`
	}
	if !decodeStrict(c, &req) {
		return ccErr(c, http.StatusBadRequest, "bad_request", "username and password are required")
	}
	u, ok := callcenterApp.NormUsername(req.Username)
	if !ok {
		return ccErr(c, http.StatusBadRequest, "bad_username", callcenterApp.ErrBadUsername.Error())
	}
	if len(req.Password) < callcenterApp.MinPassword || len(req.Password) > 128 {
		return ccErr(c, http.StatusBadRequest, "bad_password", callcenterApp.ErrBadPassword.Error())
	}
	if t, err := h.tenancy.GetTenant(ctx, tid); err == nil && t != nil && t.Username != "" {
		if strings.EqualFold(strings.TrimSpace(t.Username), u) {
			return ccErr(c, http.StatusBadRequest, "same_as_manager", "Pick a different username from the store's own login.")
		}
		if _, err := h.tenancy.AuthenticateTenant(ctx, t.Username, req.Password); err == nil {
			return ccErr(c, http.StatusBadRequest, "same_as_manager", "Pick a different password from the store's own login.")
		}
	}
	v, n, err := h.svc.SetLogin(ctx, tid, lid, u, req.Password, req.KeepDevices)
	switch {
	case errors.Is(err, callcenterApp.ErrUsernameTaken):
		return ccErr(c, http.StatusConflict, "username_taken", "That username is taken. Pick another.")
	case errors.Is(err, callcenterApp.ErrBadUsername):
		return ccErr(c, http.StatusBadRequest, "bad_username", err.Error())
	case errors.Is(err, callcenterApp.ErrBadPassword):
		return ccErr(c, http.StatusBadRequest, "bad_password", err.Error())
	case err != nil:
		return h.fail(c, err, "callcenter login")
	}
	return c.JSON(http.StatusOK, map[string]any{"username": v.Username, "set": true, "updated_at": v.UpdatedAt, "devices_revoked": n})
}

// DELETE /store/v1/me/callcenter/login — no call-center login: every browser signed out.
func (h *CallcenterHandler) MeDeleteLogin(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	if _, err := h.svc.RemoveLogin(c.Request().Context(), tid, lid); err != nil {
		return h.fail(c, err, "callcenter login")
	}
	return c.NoContent(http.StatusNoContent)
}

// GET /store/v1/me/reports/callcenter?from=&to=
func (h *CallcenterHandler) MeReport(c *echo.Context) error {
	_, lid, _ := meScope(c)
	rep, err := h.svc.Report(c.Request().Context(), lid, c.QueryParam("from"), c.QueryParam("to"))
	if errors.Is(err, callcenterApp.ErrBadRange) {
		return ccErr(c, http.StatusBadRequest, "bad_request", err.Error())
	}
	if err != nil {
		return h.fail(c, err, "callcenter report")
	}
	return c.JSON(http.StatusOK, rep)
}
