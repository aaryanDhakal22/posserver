package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"quiccpos/main/internal/app/customers"
	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// CustomersHandler serves terminal stage 0c: the admin's bulk customer
// import and the Pi's customer change feed.
type CustomersHandler struct {
	svc      *customers.Service
	location func(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
	logger   zerolog.Logger
}

func NewCustomersHandler(svc *customers.Service, location func(ctx context.Context, id uuid.UUID) (*tenancy.Location, error),
	logger zerolog.Logger) *CustomersHandler {
	return &CustomersHandler{svc: svc, location: location, logger: logger.With().Str("module", "customers-handler").Logger()}
}

// maxImportBody caps an import call's body (5,000 customers fit many times over).
const maxImportBody = 32 << 20

// Import — POST /admin/v1/locations/:id/customers/import[?dry_run=1]. The
// body is NDJSON (Content-Type application/x-ndjson or application/jsonl: one
// customer per line, blank lines ignored) or JSON (an array of customers, or
// {"customers": [...]}), up to customers.MaxImportBatch customers.
func (h *CustomersHandler) Import(c *echo.Context) error {
	lid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid location id", "bad_request"))
	}
	ctx := c.Request().Context()
	loc, err := h.location(ctx, lid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errCode("location not found", "not_found"))
		}
		h.logger.Error().Ctx(ctx).Err(err).Msg("import: location lookup")
		return c.JSON(http.StatusServiceUnavailable, errCode("try again", "try_again"))
	}
	dryRun, _ := strconv.ParseBool(c.QueryParam("dry_run"))
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxImportBody+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("could not read the body", "bad_request"))
	}
	if len(body) > maxImportBody {
		return c.JSON(http.StatusRequestEntityTooLarge, errCode("body too large", "too_large"))
	}
	ct := strings.ToLower(c.Request().Header.Get(echo.HeaderContentType))
	recs, err := decodeImport(body, strings.Contains(ct, "ndjson") || strings.Contains(ct, "jsonl"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_request"))
	}
	if len(recs) > customers.MaxImportBatch {
		return c.JSON(http.StatusRequestEntityTooLarge, errCode(customers.ErrTooManyRecords.Error(), "too_many_records"))
	}
	res, err := h.svc.Import(ctx, loc.TenantID, lid, recs, dryRun)
	if err != nil {
		h.logger.Error().Ctx(ctx).Err(err).Str("location_id", lid.String()).Msg("customer import failed")
		return c.JSON(http.StatusServiceUnavailable, errCode("the import failed and nothing was saved — try again", "try_again"))
	}
	return c.JSON(http.StatusOK, res)
}

// decodeImport reads the import body (see Import).
func decodeImport(body []byte, ndjson bool) ([]customers.ImportRecord, error) {
	if !ndjson {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			var recs []customers.ImportRecord
			if err := json.Unmarshal(trimmed, &recs); err != nil {
				return nil, fmt.Errorf("invalid JSON body: %v", err)
			}
			return recs, nil
		}
		var wrap struct {
			Customers []customers.ImportRecord `json:"customers"`
		}
		if err := json.Unmarshal(trimmed, &wrap); err != nil {
			return nil, fmt.Errorf("invalid JSON body (send an array, {\"customers\": [...]}, or NDJSON as application/x-ndjson): %v", err)
		}
		return wrap.Customers, nil
	}
	var recs []customers.ImportRecord
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var r customers.ImportRecord
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("line %d: invalid JSON: %v", line, err)
		}
		recs = append(recs, r)
		if len(recs) > customers.MaxImportBatch {
			return recs, nil // the caller refuses the batch
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("line %d: %v", line+1, err)
	}
	return recs, nil
}

// Feed — GET /store/v1/agent/customers?since=<cursor>&limit=<n> (agent key):
// the store's customer changes after the cursor, for the Pi's offline copy.
func (h *CustomersHandler) Feed(c *echo.Context) error {
	scope, _, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	limit := 500
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return c.JSON(http.StatusBadRequest, errCode("limit must be a positive number", "bad_request"))
		}
		limit = min(n, 2000)
	}
	feed, err := h.svc.Changes(c.Request().Context(), scope.LocationID, c.QueryParam("since"), limit)
	if errors.Is(err, customers.ErrBadCursor) {
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_cursor"))
	}
	if err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("customer feed")
		return c.JSON(http.StatusServiceUnavailable, errCode("try again", "try_again"))
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, feed)
}
