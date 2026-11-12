package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"quiccpos/main/internal/app/bankimport"
	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// maxBankDoc caps a bank document (a large store's bank + views + deals is a
// few hundred KB).
const maxBankDoc = 8 << 20

// BankImportHandler serves the bank document: export and (dry-run) apply.
type BankImportHandler struct {
	svc    *bankimport.Service
	logger zerolog.Logger
}

func NewBankImportHandler(svc *bankimport.Service, logger zerolog.Logger) *BankImportHandler {
	return &BankImportHandler{svc: svc, logger: logger.With().Str("module", "bankimport-handler").Logger()}
}

// GET /store/v1/me/menu/bank/document — the store's bank, views and deals as
// one document (names, no ids).
func (h *BankImportHandler) MeExport(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	doc, err := h.svc.Export(c.Request().Context(), tid, lid)
	if err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("bank export failed")
		return c.JSON(http.StatusInternalServerError, errCode("export failed", "internal"))
	}
	return c.JSON(http.StatusOK, doc)
}

// POST /store/v1/me/menu/bank/apply?dry_run=1&prune=1 — apply a document in
// one transaction (dry_run: apply, report, roll back). 400 bad_document with
// the path of the first problem; nothing is written then.
func (h *BankImportHandler) MeApply(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxBankDoc+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("could not read the document", "bad_request"))
	}
	if len(body) > maxBankDoc {
		return c.JSON(http.StatusRequestEntityTooLarge, errCode("document too large (max 8 MB)", "too_large"))
	}
	var doc bankdoc.Document
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("document: "+err.Error(), "bad_document"))
	}
	o := bankimport.Options{DryRun: truthy(c.QueryParam("dry_run")), Prune: truthy(c.QueryParam("prune"))}
	res, err := h.svc.Apply(c.Request().Context(), tid, lid, &doc, o)
	switch {
	case err == nil:
		return c.JSON(http.StatusOK, res)
	case errors.Is(err, bankimport.ErrBadDocument):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_document"))
	case errors.Is(err, repositories.ErrCodeTaken):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "code_taken"))
	}
	h.logger.Error().Ctx(c.Request().Context()).Err(err).Bool("dry_run", o.DryRun).Msg("bank apply failed")
	return c.JSON(http.StatusInternalServerError, errCode("apply failed (nothing was written): "+err.Error(), "internal"))
}
