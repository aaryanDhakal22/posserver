package siteApp

// The Website page: the store edits its look as a draft, previews it on the
// storefront with a private link, and publishes it (GET /channel/v1/site
// then serves it with a new version, which the storefront picks up within
// its refresh). Logos and cover photos are small uploads kept in Postgres.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"quiccpos/main/internal/domain/site"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrNothingToPublish = errors.New("there is no draft to publish")
	ErrBadPreview       = errors.New("that preview link is no longer valid")
	ErrImageNotFound    = errors.New("image not found")
)

// WebsiteRepository is the Website page's storage (repositories.SiteRepository).
type WebsiteRepository interface {
	SaveDraft(ctx context.Context, tenantID, locationID uuid.UUID, draft []byte) error
	DiscardDraft(ctx context.Context, locationID uuid.UUID) error
	Publish(ctx context.Context, locationID uuid.UUID, by string) (int64, error)
	PreviewToken(ctx context.Context, tenantID, locationID uuid.UUID, fresh string) (string, error)
	AddImage(ctx context.Context, tenantID, locationID uuid.UUID, img site.Image) (site.Image, error)
	Image(ctx context.Context, locationID, id uuid.UUID) (site.Image, error)
	DropUnusedImages(ctx context.Context, locationID uuid.UUID, keep []uuid.UUID) (int64, error)
}

// SetPreviewOrigin sets how a host becomes the preview's origin ("{host}" is
// replaced; "" = https://{host}). Dev's storefront runs on another port.
func (s *Service) SetPreviewOrigin(tmpl string) {
	if strings.Contains(tmpl, "{host}") {
		s.previewOrigin = tmpl
	}
}

func (s *Service) website() (WebsiteRepository, error) {
	w, ok := s.repo.(WebsiteRepository)
	if !ok {
		return nil, errors.New("site repository has no website storage")
	}
	return w, nil
}

// Website is what the Website page edits.
type Website struct {
	// Draft is what the editor shows: the open draft, else the published look.
	Draft     json.RawMessage `json:"draft"`
	Published json.RawMessage `json:"published"`
	HasDraft  bool            `json:"has_draft"`
	// Changes are the top-level fields the draft changes (empty = none).
	Changes     []string   `json:"changes"`
	Version     int64      `json:"version"`
	DraftAt     *time.Time `json:"draft_at"`
	PublishedAt *time.Time `json:"published_at"`
	PublishedBy string     `json:"published_by"`
	// PreviewURL opens the draft on the storefront: <origin>/p/<token>, then
	// "/menu" etc. for another page ("" = the store has no host yet).
	PreviewURL  string `json:"preview_url"`
	PreviewHost string `json:"preview_host"`
}

func orEmpty(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

// changes lists the top-level fields that differ between two documents.
func changes(draft, published []byte) []string {
	var a, b map[string]any
	_ = json.Unmarshal(draft, &a)
	_ = json.Unmarshal(published, &b)
	out := []string{}
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	for k := range seen {
		if !reflect.DeepEqual(a[k], b[k]) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) tenantOf(ctx context.Context, locationID uuid.UUID) (uuid.UUID, error) {
	loc, err := s.locs.GetLocation(ctx, locationID)
	if err != nil {
		return uuid.Nil, ErrUnknownPlace
	}
	return loc.TenantID, nil
}

// Website is the store's Website page state (it makes the preview token on first use).
func (s *Service) Website(ctx context.Context, locationID uuid.UUID) (Website, error) {
	w, err := s.website()
	if err != nil {
		return Website{}, err
	}
	tenantID, err := s.tenantOf(ctx, locationID)
	if err != nil {
		return Website{}, err
	}
	st, err := s.repo.Settings(ctx, locationID)
	if err != nil {
		return Website{}, err
	}
	token := st.PreviewToken
	if token == "" {
		buf := make([]byte, 18)
		if _, err := rand.Read(buf); err != nil {
			return Website{}, err
		}
		if token, err = w.PreviewToken(ctx, tenantID, locationID, "qpv_"+hex.EncodeToString(buf)); err != nil {
			return Website{}, err
		}
	}
	out := Website{Published: orEmpty(st.Published), HasDraft: st.DraftAt != nil, Version: st.Version, DraftAt: st.DraftAt,
		PublishedAt: st.PublishedAt, PublishedBy: st.PublishedBy, Changes: []string{}}
	out.Draft = out.Published
	if out.HasDraft {
		out.Draft = orEmpty(st.Draft)
		out.Changes = changes(out.Draft, out.Published)
	}
	if host := s.previewHost(ctx, locationID); host != "" {
		origin := "https://" + host
		if s.previewOrigin != "" {
			origin = strings.ReplaceAll(s.previewOrigin, "{host}", host)
		}
		out.PreviewHost = host
		out.PreviewURL = origin + "/p/" + token
	}
	return out, nil
}

// previewHost is the host the preview opens on: a verified host on the
// platform's domain first (its cookie is same-site with the admin), else the
// primary one.
func (s *Service) previewHost(ctx context.Context, locationID uuid.UUID) string {
	ds, err := s.repo.Domains(ctx, locationID)
	if err != nil {
		return ""
	}
	primary := ""
	for _, d := range ds {
		if !d.Verified {
			continue
		}
		if site.OnPlatform(d.Host, s.baseDomain) || strings.HasSuffix(d.Host, ".localhost") {
			return d.Host
		}
		if d.Primary || primary == "" {
			primary = d.Host
		}
	}
	return primary
}

// SaveDraft checks a document and makes it the store's draft.
func (s *Service) SaveDraft(ctx context.Context, locationID uuid.UUID, raw []byte) (Website, error) {
	w, err := s.website()
	if err != nil {
		return Website{}, err
	}
	tenantID, err := s.tenantOf(ctx, locationID)
	if err != nil {
		return Website{}, err
	}
	look, norm, err := site.ParseLook(raw)
	if err != nil {
		return Website{}, err
	}
	for _, x := range []struct{ kind, id string }{{"logo", look.Logo}, {"cover", look.Cover}} {
		if x.id == "" {
			continue
		}
		img, err := w.Image(ctx, locationID, uuid.MustParse(x.id))
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && img.Kind != x.kind) {
			return Website{}, errors.Join(site.ErrBadLook, errors.New(x.kind+" must be one of this store's uploaded "+x.kind+" images"))
		}
		if err != nil {
			return Website{}, err
		}
	}
	if err := w.SaveDraft(ctx, tenantID, locationID, norm); err != nil {
		return Website{}, err
	}
	return s.Website(ctx, locationID)
}

// DiscardDraft drops the store's draft (the published look stays).
func (s *Service) DiscardDraft(ctx context.Context, locationID uuid.UUID) (Website, error) {
	w, err := s.website()
	if err != nil {
		return Website{}, err
	}
	if err := w.DiscardDraft(ctx, locationID); err != nil {
		return Website{}, err
	}
	s.dropUnused(ctx, locationID)
	return s.Website(ctx, locationID)
}

// Publish makes the draft the store's website (version + 1).
func (s *Service) Publish(ctx context.Context, locationID uuid.UUID, by string) (Website, error) {
	w, err := s.website()
	if err != nil {
		return Website{}, err
	}
	v, err := w.Publish(ctx, locationID, by)
	if errors.Is(err, pgx.ErrNoRows) {
		return Website{}, ErrNothingToPublish
	}
	if err != nil {
		return Website{}, err
	}
	s.logger.Info().Str("location_id", locationID.String()).Int64("version", v).Str("by", by).Msg("website published")
	s.dropUnused(ctx, locationID)
	if tenantID, err := s.tenantOf(ctx, locationID); err == nil {
		for _, fn := range s.onPublished {
			fn(ctx, tenantID, locationID)
		}
	}
	return s.Website(ctx, locationID)
}

// dropUnused deletes images neither the draft nor the published look uses
// (only those older than a day: a fresh upload may not be in the draft yet).
func (s *Service) dropUnused(ctx context.Context, locationID uuid.UUID) {
	w, err := s.website()
	if err != nil {
		return
	}
	st, err := s.repo.Settings(ctx, locationID)
	if err != nil {
		return
	}
	var keep []uuid.UUID
	for _, doc := range [][]byte{st.Draft, st.Published} {
		var l site.Look
		if json.Unmarshal(doc, &l) != nil {
			continue
		}
		for _, id := range l.Images() {
			if u, err := uuid.Parse(id); err == nil {
				keep = append(keep, u)
			}
		}
	}
	if n, err := w.DropUnusedImages(ctx, locationID, keep); err != nil {
		s.logger.Warn().Err(err).Str("location_id", locationID.String()).Msg("unused site images not dropped")
	} else if n > 0 {
		s.logger.Info().Int64("dropped", n).Str("location_id", locationID.String()).Msg("unused site images dropped")
	}
}

// AddImage stores a logo or cover photo (its real type is sniffed).
func (s *Service) AddImage(ctx context.Context, locationID uuid.UUID, kind string, data []byte) (site.Image, error) {
	w, err := s.website()
	if err != nil {
		return site.Image{}, err
	}
	if kind != "logo" && kind != "cover" {
		return site.Image{}, site.ErrBadImage
	}
	ct := site.SniffImage(kind, data)
	if ct == "" || len(data) == 0 || len(data) > site.MaxImageBytes {
		return site.Image{}, site.ErrBadImage
	}
	tenantID, err := s.tenantOf(ctx, locationID)
	if err != nil {
		return site.Image{}, err
	}
	return w.AddImage(ctx, tenantID, locationID, site.Image{Kind: kind, ContentType: ct, Bytes: data})
}

// Image is one of the store's images.
func (s *Service) Image(ctx context.Context, locationID uuid.UUID, id string) (site.Image, error) {
	w, err := s.website()
	if err != nil {
		return site.Image{}, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return site.Image{}, ErrImageNotFound
	}
	img, err := w.Image(ctx, locationID, u)
	if errors.Is(err, pgx.ErrNoRows) {
		return img, ErrImageNotFound
	}
	return img, err
}

// SiteFor is GET /channel/v1/site; a valid preview token answers with the
// draft (preview: true), a stale one is ErrBadPreview.
func (s *Service) SiteFor(ctx context.Context, locationID uuid.UUID, preview string) (View, error) {
	v, err := s.Site(ctx, locationID)
	if err != nil || preview == "" {
		return v, err
	}
	st, err := s.repo.Settings(ctx, locationID)
	if err != nil {
		return v, err
	}
	if st.PreviewToken == "" || subtle.ConstantTimeCompare([]byte(preview), []byte(st.PreviewToken)) != 1 {
		return View{}, ErrBadPreview
	}
	v.Preview = true
	if st.DraftAt != nil {
		v.Settings = jsonRaw(st.Draft)
	}
	return v, nil
}
