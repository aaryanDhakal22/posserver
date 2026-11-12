package siteApp

// The receipt logo: the store's PUBLISHED Website logo, handed to the Pi in
// store_config (receipt_logo) and served by GET /store/v1/agent/receipt-logo,
// so the Pi prints it at the top of its receipts. Only a raster image can be
// printed: an SVG logo (or none) means no receipt logo.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"quiccpos/main/internal/domain/site"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PrintableLogo says whether a logo of this type can go on a receipt.
func PrintableLogo(contentType string) bool {
	switch contentType {
	case "image/png", "image/jpeg", "image/webp":
		return true
	}
	return false
}

// LogoETag is the receipt logo's ETag (quoted, as the HTTP header carries it).
func LogoETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"l` + hex.EncodeToString(sum[:8]) + `"`
}

// receiptLogo picks the printable logo out of a published look; nil when the
// look has none, it is an SVG, or the image is gone.
func receiptLogo(published []byte, image func(uuid.UUID) (site.Image, error)) (*site.Image, error) {
	var l site.Look
	if len(published) == 0 || json.Unmarshal(published, &l) != nil || l.Logo == "" {
		return nil, nil
	}
	id, err := uuid.Parse(l.Logo)
	if err != nil {
		return nil, nil
	}
	img, err := image(id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if img.Kind != "logo" || !PrintableLogo(img.ContentType) || len(img.Bytes) == 0 {
		return nil, nil
	}
	return &img, nil
}

// ReceiptLogo is the store's published logo when a receipt can print it
// (PNG, JPEG or WebP); nil when there is none or it is an SVG.
func (s *Service) ReceiptLogo(ctx context.Context, locationID uuid.UUID) (*site.Image, error) {
	w, err := s.website()
	if err != nil {
		return nil, err
	}
	st, err := s.repo.Settings(ctx, locationID)
	if err != nil {
		return nil, err
	}
	return receiptLogo(st.Published, func(id uuid.UUID) (site.Image, error) { return w.Image(ctx, locationID, id) })
}

// OnPublished registers a hook run after each publish (cmd/api pushes the
// store's store_config, so the Pi picks up a new receipt logo at once).
func (s *Service) OnPublished(fn func(ctx context.Context, tenantID, locationID uuid.UUID)) {
	s.onPublished = append(s.onPublished, fn)
}
