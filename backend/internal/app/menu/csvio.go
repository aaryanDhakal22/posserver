package menuApp

// Menu CSV import/export — the bulk path for building a location's menu from
// the vendor's item names (and for editing it in a spreadsheet).
//
// Columns (header row required, case-insensitive, extra columns ignored):
//
//	menu         menu name (optional, default "Main")
//	category     category name (required)
//	item         item name exactly as the vendor sends it (required; case and
//	             repeated spaces don't matter — routing normalizes the same way)
//	price        dollars, e.g. 12.50 or $12.50 (optional)
//	printer      station printer name as set up in /app/store (optional)
//	description  (optional)
//
// Rules: menus, categories and items are matched by normalized name and
// reused, so a re-import is idempotent. A blank price/printer/description cell
// leaves the existing value alone (a new item gets 0 / none / ""). A new
// category (or one with no default printer) gets the printer most of its rows
// name as its default; an item gets its own printer only where it differs from
// the category default. Nothing is deleted unless replace=true, which deletes
// every menu of the location first. The import is not one transaction — every
// row is validated (printers included) before the first write, and a re-run
// after a failure picks up where it stopped.

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	menuDom "quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

// DefaultImportMenu is the menu rows without a `menu` cell land in.
const DefaultImportMenu = "Main"

// MaxImportRows bounds one import.
const MaxImportRows = 5000

var exportHeader = []string{"menu", "category", "item", "price", "printer", "description"}

// CSVRow is one parsed import row. Has* report a non-blank cell.
type CSVRow struct {
	Line                     int
	Menu, Category, Item     string
	Printer, Description     string
	PriceCents               int64
	HasPrice, HasDescription bool
}

// CSVError is a parse or validation failure the caller should fix in the file.
type CSVError struct {
	Line int // 0 = not tied to one row
	Msg  string
}

func (e *CSVError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

// IsCSVError lets handlers map import input errors to 400.
func IsCSVError(err error) bool {
	var ce *CSVError
	return errors.As(err, &ce)
}

// ParseMenuCSV reads an import file.
func ParseMenuCSV(r io.Reader) ([]CSVRow, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, &CSVError{Msg: "the file is empty"}
	}
	if err != nil {
		return nil, &CSVError{Msg: "unreadable CSV: " + err.Error()}
	}
	col := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		if _, dup := col[h]; !dup && h != "" {
			col[h] = i
		}
	}
	for _, need := range []string{"category", "item"} {
		if _, ok := col[need]; !ok {
			return nil, &CSVError{Msg: fmt.Sprintf("missing column %q (header: %s)", need, strings.Join(header, ","))}
		}
	}
	cell := func(rec []string, name string) (string, bool) {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return "", false
		}
		v := strings.TrimSpace(rec[i])
		return v, v != ""
	}

	var rows []CSVRow
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// FieldPos is only valid for a successfully read record (it
			// panics after a parse error in the first field).
			line := 0
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				line = pe.Line
			}
			return nil, &CSVError{Line: line, Msg: err.Error()}
		}
		line, _ := cr.FieldPos(0)
		if blankRecord(rec) {
			continue
		}
		row := CSVRow{Line: line}
		row.Menu, _ = cell(rec, "menu")
		if row.Menu == "" {
			row.Menu = DefaultImportMenu
		}
		row.Category, _ = cell(rec, "category")
		row.Item, _ = cell(rec, "item")
		if row.Category == "" || row.Item == "" {
			return nil, &CSVError{Line: line, Msg: "category and item are required"}
		}
		row.Printer, _ = cell(rec, "printer")
		row.Description, row.HasDescription = cell(rec, "description")
		if p, ok := cell(rec, "price"); ok {
			cents, err := parsePrice(p)
			if err != nil {
				return nil, &CSVError{Line: line, Msg: err.Error()}
			}
			row.PriceCents, row.HasPrice = cents, true
		}
		rows = append(rows, row)
		if len(rows) > MaxImportRows {
			return nil, &CSVError{Msg: fmt.Sprintf("too many rows (max %d)", MaxImportRows)}
		}
	}
	if len(rows) == 0 {
		return nil, &CSVError{Msg: "the file has no rows"}
	}
	return rows, nil
}

func blankRecord(rec []string) bool {
	for _, v := range rec {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

func parsePrice(s string) (int64, error) {
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimPrefix(s, "$"), ",", ""), 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) || f > 1e7 {
		return 0, fmt.Errorf("bad price %q", s)
	}
	return int64(math.Round(f * 100)), nil
}

func formatPrice(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// ImportResult summarizes an import (or, with DryRun, what it would do).
type ImportResult struct {
	DryRun           bool `json:"dry_run"`
	Replace          bool `json:"replace"`
	Rows             int  `json:"rows"`
	MenusDeleted     int  `json:"menus_deleted"`
	Menus            int  `json:"menus"`
	Categories       int  `json:"categories"`
	CategoryPrinters int  `json:"category_printers"`
	Items            int  `json:"items"`
	ItemsUpdated     int  `json:"items_updated"`
	ItemPrinters     int  `json:"item_printers"`
	// PrintsOn counts the file's items by the printer they will print on
	// (their own printer, else their category's default), in the store's
	// printer order; printers no item prints on are left out.
	PrintsOn []PrinterItems `json:"prints_on"`
	// ItemsNoPrinter counts the file's items left without any printer — they
	// print on every kitchen station.
	ItemsNoPrinter int      `json:"items_no_printer"`
	Changes        []string `json:"changes"`
}

// PrinterItems is one entry of ImportResult.PrintsOn.
type PrinterItems struct {
	Printer string `json:"printer"`
	Items   int    `json:"items"`
}

func (r *ImportResult) note(format string, a ...any) {
	r.Changes = append(r.Changes, fmt.Sprintf(format, a...))
}

// ImportCSV applies parsed rows to a location's menu. See the package comment
// above for the rules. Unknown printer names fail the whole import up front.
func (s *Service) ImportCSV(ctx context.Context, tenantID, locationID uuid.UUID, rows []CSVRow, dryRun, replace bool) (res *ImportResult, err error) {
	ctx, span := s.tracer.Start(ctx, "menu.import")
	defer span.End()
	span.SetAttributes(attribute.Int("menu.import.rows", len(rows)), attribute.Bool("menu.import.dry_run", dryRun), attribute.Bool("menu.import.replace", replace))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "menu import failed")
		} else if !dryRun {
			s.meters.MenuMutations.Add(ctx, 1, metric.WithAttributes(attribute.String("entity", "menu"), attribute.String("op", "import")))
		}
	}()

	printers, err := s.listPrinters(ctx, locationID)
	if err != nil {
		return nil, fmt.Errorf("resolve printers: %w", err)
	}
	printerID := map[string]uuid.UUID{}
	printerName := map[uuid.UUID]string{}
	var have []string
	for _, p := range printers {
		printerID[normalizeItemName(p.Name)] = p.ID
		printerName[p.ID] = p.Name
		have = append(have, p.Name)
	}
	var unknown []string
	seenUnknown := map[string]bool{}
	for _, r := range rows {
		k := normalizeItemName(r.Printer)
		if r.Printer != "" && printerID[k] == uuid.Nil && !seenUnknown[k] {
			seenUnknown[k] = true
			unknown = append(unknown, r.Printer)
		}
	}
	if len(unknown) > 0 {
		msg := fmt.Sprintf("This file sends items to printers that this store doesn't have yet: %s. "+
			"Add them on the Printers page first, or clear the printer column.", strings.Join(unknown, ", "))
		if len(have) > 0 {
			msg += " This store has: " + strings.Join(have, ", ") + "."
		}
		return nil, &CSVError{Msg: msg}
	}

	res = &ImportResult{DryRun: dryRun, Replace: replace, Rows: len(rows), PrintsOn: []PrinterItems{}, Changes: []string{}}
	printsOn := map[uuid.UUID]int{}
	defer func() {
		for _, p := range printers {
			if n := printsOn[p.ID]; n > 0 {
				res.PrintsOn = append(res.PrintsOn, PrinterItems{Printer: p.Name, Items: n})
			}
		}
	}()
	menus, err := s.repo.ListMenusByLocation(ctx, locationID)
	if err != nil {
		return nil, err
	}
	if replace {
		for _, m := range menus {
			res.MenusDeleted++
			res.note("- menu %q (and everything in it)", m.Name)
			if !dryRun {
				if err := s.repo.DeleteMenu(ctx, m.ID, tenantID); err != nil {
					return res, fmt.Errorf("delete menu %q: %w", m.Name, err)
				}
			}
		}
		menus = nil
	}

	// Group rows: menu → category → rows, in file order.
	type catRows struct {
		name string
		rows []CSVRow
	}
	type menuRows struct {
		name string
		cats []*catRows
		idx  map[string]*catRows
	}
	var order []*menuRows
	byMenu := map[string]*menuRows{}
	for _, r := range rows {
		mk := normalizeItemName(r.Menu)
		m := byMenu[mk]
		if m == nil {
			m = &menuRows{name: r.Menu, idx: map[string]*catRows{}}
			byMenu[mk] = m
			order = append(order, m)
		}
		ck := normalizeItemName(r.Category)
		c := m.idx[ck]
		if c == nil {
			c = &catRows{name: r.Category}
			m.idx[ck] = c
			m.cats = append(m.cats, c)
		}
		c.rows = append(c.rows, r)
	}

	existingMenus := map[string]menuDom.Menu{}
	for _, m := range menus {
		if k := normalizeItemName(m.Name); existingMenus[k].ID == uuid.Nil {
			existingMenus[k] = m
		}
	}
	nextMenuPos := len(menus)

	for _, mr := range order {
		menu, found := existingMenus[normalizeItemName(mr.name)]
		if !found {
			res.Menus++
			res.note("+ menu %q", mr.name)
			if !dryRun {
				m, err := s.repo.CreateMenu(ctx, menuDom.CreateMenuParams{TenantID: tenantID, LocationID: locationID, Name: mr.name, Available: true, Position: nextMenuPos})
				if err != nil {
					return res, fmt.Errorf("create menu %q: %w", mr.name, err)
				}
				menu = *m
			}
			nextMenuPos++
		}

		var cats []menuDom.Category
		if menu.ID != uuid.Nil {
			if cats, err = s.repo.ListCategoriesByMenu(ctx, menu.ID); err != nil {
				return res, err
			}
		}
		existingCats := map[string]menuDom.Category{}
		for _, c := range cats {
			if k := normalizeItemName(c.Name); existingCats[k].ID == uuid.Nil {
				existingCats[k] = c
			}
		}
		nextCatPos := len(cats)

		for _, cr := range mr.cats {
			want := mostCommonPrinter(cr.rows, printerID)
			cat, found := existingCats[normalizeItemName(cr.name)]
			switch {
			case !found:
				res.Categories++
				if want != nil {
					res.CategoryPrinters++
					res.note("+ category %q (printer %s)", cr.name, printerName[*want])
				} else {
					res.note("+ category %q", cr.name)
				}
				cat = menuDom.Category{Name: cr.name, DefaultPrinterID: want}
				if !dryRun {
					c, err := s.repo.CreateCategory(ctx, menuDom.CreateCategoryParams{
						TenantID: tenantID, LocationID: locationID, MenuID: menu.ID, Name: cr.name,
						Available: true, Position: nextCatPos, DefaultPrinterID: want,
					})
					if err != nil {
						return res, fmt.Errorf("create category %q: %w", cr.name, err)
					}
					cat = *c
				}
				nextCatPos++
			case cat.DefaultPrinterID == nil && want != nil:
				// Keep an existing default; only fill one in.
				res.CategoryPrinters++
				res.note("~ category %q printer → %s", cat.Name, printerName[*want])
				if !dryRun {
					c, err := s.repo.UpdateCategory(ctx, menuDom.UpdateCategoryParams{
						ID: cat.ID, TenantID: tenantID, Name: cat.Name, Description: cat.Description,
						Available: cat.Available, DefaultPrinterID: want,
					})
					if err != nil {
						return res, fmt.Errorf("update category %q: %w", cat.Name, err)
					}
					cat = *c
				} else {
					cat.DefaultPrinterID = want
				}
			}

			var items []menuDom.Item
			if cat.ID != uuid.Nil {
				if items, err = s.repo.ListItemsByCategory(ctx, cat.ID); err != nil {
					return res, err
				}
			}
			existingItems := map[string]*menuDom.Item{}
			for i := range items {
				if k := normalizeItemName(items[i].Name); existingItems[k] == nil {
					existingItems[k] = &items[i]
				}
			}
			nextItemPos := len(items)
			// The category's items this file names, for res.PrintsOn.
			var fileItems []*menuDom.Item
			inFile := map[*menuDom.Item]bool{}

			for _, r := range cr.rows {
				k := normalizeItemName(r.Item)
				it := existingItems[k]
				if it == nil {
					res.Items++
					res.note("  + %q $%s", r.Item, formatPrice(r.PriceCents))
					it = &menuDom.Item{Name: r.Item, PriceCents: r.PriceCents, Description: r.Description, InStock: true}
					if !dryRun {
						created, err := s.repo.CreateItem(ctx, menuDom.CreateItemParams{
							TenantID: tenantID, LocationID: locationID, CategoryID: cat.ID, Name: r.Item,
							Description: r.Description, PriceCents: r.PriceCents, InStock: true, Position: nextItemPos,
						})
						if err != nil {
							return res, fmt.Errorf("line %d: create item %q: %w", r.Line, r.Item, err)
						}
						it = created
					}
					nextItemPos++
					existingItems[k] = it
				} else if (r.HasPrice && r.PriceCents != it.PriceCents) || (r.HasDescription && r.Description != it.Description) {
					res.ItemsUpdated++
					next := *it
					if r.HasPrice {
						next.PriceCents = r.PriceCents
					}
					if r.HasDescription {
						next.Description = r.Description
					}
					res.note("  ~ %q $%s", it.Name, formatPrice(next.PriceCents))
					if !dryRun {
						updated, err := s.repo.UpdateItem(ctx, menuDom.UpdateItemParams{
							ID: it.ID, TenantID: tenantID, Name: it.Name, Description: next.Description,
							PriceCents: next.PriceCents, TaxRateBps: it.TaxRateBps, InStock: it.InStock,
							OutUntil: it.OutUntil, Tags: it.Tags, Attributes: it.Attributes,
						})
						if err != nil {
							return res, fmt.Errorf("line %d: update item %q: %w", r.Line, it.Name, err)
						}
						next = *updated
					}
					*it = next
				}
				if !inFile[it] {
					inFile[it] = true
					fileItems = append(fileItems, it)
				}

				if r.Printer == "" {
					continue // blank = leave the item's printer alone
				}
				pid := printerID[normalizeItemName(r.Printer)]
				var target *uuid.UUID // nil = inherit the category default
				if cat.DefaultPrinterID == nil || *cat.DefaultPrinterID != pid {
					target = &pid
				}
				if samePrinter(target, it.PrinterID) {
					continue
				}
				res.ItemPrinters++
				if target != nil {
					res.note("  ~ %q printer → %s", it.Name, printerName[pid])
				} else {
					res.note("  ~ %q printer → category default", it.Name)
				}
				if !dryRun {
					if err := s.repo.SetItemPrinter(ctx, it.ID, tenantID, target); err != nil {
						return res, fmt.Errorf("line %d: set printer for %q: %w", r.Line, it.Name, err)
					}
				}
				it.PrinterID = target
			}
			for _, it := range fileItems {
				switch {
				case it.PrinterID != nil:
					printsOn[*it.PrinterID]++
				case cat.DefaultPrinterID != nil:
					printsOn[*cat.DefaultPrinterID]++
				default:
					res.ItemsNoPrinter++
				}
			}
		}
	}
	return res, nil
}

// mostCommonPrinter is the printer most rows name (first named wins a tie).
func mostCommonPrinter(rows []CSVRow, printerID map[string]uuid.UUID) *uuid.UUID {
	counts := map[uuid.UUID]int{}
	var best uuid.UUID
	for _, r := range rows {
		if r.Printer == "" {
			continue
		}
		id := printerID[normalizeItemName(r.Printer)]
		counts[id]++
		if best == uuid.Nil || counts[id] > counts[best] {
			best = id
		}
	}
	if best == uuid.Nil {
		return nil
	}
	return &best
}

func samePrinter(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ExportCSV writes the location's menus as an import file. The printer column
// is each item's effective printer (its own, else the category default), so
// export → import reproduces the routing.
func (s *Service) ExportCSV(ctx context.Context, locationID uuid.UUID, w io.Writer) error {
	ctx, span := s.tracer.Start(ctx, "menu.export")
	defer span.End()
	printers, err := s.listPrinters(ctx, locationID)
	if err != nil {
		return fmt.Errorf("resolve printers: %w", err)
	}
	printerName := map[uuid.UUID]string{}
	for _, p := range printers {
		printerName[p.ID] = p.Name
	}
	menus, err := s.repo.ListMenusByLocation(ctx, locationID)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(exportHeader); err != nil {
		return err
	}
	for _, m := range menus {
		cats, err := s.repo.ListCategoriesByMenu(ctx, m.ID)
		if err != nil {
			return err
		}
		for _, c := range cats {
			items, err := s.repo.ListItemsByCategory(ctx, c.ID)
			if err != nil {
				return err
			}
			for _, it := range items {
				printer := ""
				if it.PrinterID != nil {
					printer = printerName[*it.PrinterID]
				} else if c.DefaultPrinterID != nil {
					printer = printerName[*c.DefaultPrinterID]
				}
				if err := cw.Write([]string{m.Name, c.Name, it.Name, formatPrice(it.PriceCents), printer, it.Description}); err != nil {
					return err
				}
			}
		}
	}
	cw.Flush()
	return cw.Error()
}
