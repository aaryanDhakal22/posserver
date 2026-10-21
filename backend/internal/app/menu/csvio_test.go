package menuApp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	menuDom "quiccpos/main/internal/domain/menu"
	tenancyDom "quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// memRepo is an in-memory menu store for the import/export tests.
type memRepo struct {
	menuDom.Repository
	menus []*menuDom.Menu
	cats  []*menuDom.Category
	items []*menuDom.Item
	calls int // writes
}

func (r *memRepo) ListMenusByLocation(_ context.Context, loc uuid.UUID) ([]menuDom.Menu, error) {
	var out []menuDom.Menu
	for _, m := range r.menus {
		if m.LocationID == loc {
			out = append(out, *m)
		}
	}
	return out, nil
}
func (r *memRepo) CreateMenu(_ context.Context, p menuDom.CreateMenuParams) (*menuDom.Menu, error) {
	r.calls++
	m := &menuDom.Menu{ID: uuid.New(), TenantID: p.TenantID, LocationID: p.LocationID, Name: p.Name, Available: p.Available, Position: p.Position}
	r.menus = append(r.menus, m)
	return m, nil
}
func (r *memRepo) DeleteMenu(_ context.Context, id, _ uuid.UUID) error {
	r.calls++
	var keep []*menuDom.Menu
	for _, m := range r.menus {
		if m.ID != id {
			keep = append(keep, m)
		}
	}
	r.menus = keep
	var cats []*menuDom.Category
	for _, c := range r.cats {
		if c.MenuID != id {
			cats = append(cats, c)
		} else {
			var items []*menuDom.Item
			for _, it := range r.items {
				if it.CategoryID != c.ID {
					items = append(items, it)
				}
			}
			r.items = items
		}
	}
	r.cats = cats
	return nil
}
func (r *memRepo) ListCategoriesByMenu(_ context.Context, menuID uuid.UUID) ([]menuDom.Category, error) {
	var out []menuDom.Category
	for _, c := range r.cats {
		if c.MenuID == menuID {
			out = append(out, *c)
		}
	}
	return out, nil
}
func (r *memRepo) CreateCategory(_ context.Context, p menuDom.CreateCategoryParams) (*menuDom.Category, error) {
	r.calls++
	c := &menuDom.Category{ID: uuid.New(), TenantID: p.TenantID, LocationID: p.LocationID, MenuID: p.MenuID, Name: p.Name, Available: p.Available, DefaultPrinterID: p.DefaultPrinterID}
	r.cats = append(r.cats, c)
	return c, nil
}
func (r *memRepo) UpdateCategory(_ context.Context, p menuDom.UpdateCategoryParams) (*menuDom.Category, error) {
	r.calls++
	for _, c := range r.cats {
		if c.ID == p.ID {
			c.Name, c.Description, c.Available, c.DefaultPrinterID = p.Name, p.Description, p.Available, p.DefaultPrinterID
			return c, nil
		}
	}
	return nil, errNotFound
}
func (r *memRepo) ListItemsByCategory(_ context.Context, catID uuid.UUID) ([]menuDom.Item, error) {
	var out []menuDom.Item
	for _, it := range r.items {
		if it.CategoryID == catID {
			out = append(out, *it)
		}
	}
	return out, nil
}
func (r *memRepo) CreateItem(_ context.Context, p menuDom.CreateItemParams) (*menuDom.Item, error) {
	r.calls++
	it := &menuDom.Item{ID: uuid.New(), TenantID: p.TenantID, LocationID: p.LocationID, CategoryID: p.CategoryID, Name: p.Name, Description: p.Description, PriceCents: p.PriceCents, InStock: p.InStock}
	r.items = append(r.items, it)
	return it, nil
}
func (r *memRepo) UpdateItem(_ context.Context, p menuDom.UpdateItemParams) (*menuDom.Item, error) {
	r.calls++
	for _, it := range r.items {
		if it.ID == p.ID {
			it.Name, it.Description, it.PriceCents = p.Name, p.Description, p.PriceCents
			return it, nil
		}
	}
	return nil, errNotFound
}
func (r *memRepo) SetItemPrinter(_ context.Context, id, _ uuid.UUID, printerID *uuid.UUID) error {
	r.calls++
	for _, it := range r.items {
		if it.ID == id {
			it.PrinterID = printerID
			return nil
		}
	}
	return errNotFound
}

func newImportSvc(t *testing.T, printers ...string) (*Service, *memRepo, map[string]uuid.UUID) {
	t.Helper()
	repo := &memRepo{}
	meters, _ := observability.NewMeters()
	svc := NewService(repo, nil, meters, zerolog.Nop())
	ids := map[string]uuid.UUID{}
	var ps []tenancyDom.Printer
	for _, n := range printers {
		ids[n] = uuid.New()
		ps = append(ps, tenancyDom.Printer{ID: ids[n], Name: n})
	}
	svc.listPrinters = func(context.Context, uuid.UUID) ([]tenancyDom.Printer, error) { return ps, nil }
	return svc, repo, ids
}

func mustParse(t *testing.T, s string) []CSVRow {
	t.Helper()
	rows, err := ParseMenuCSV(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestParseMenuCSV(t *testing.T) {
	rows := mustParse(t, "\ufeffCategory,Item,Price,Printer,sizes\n"+
		"Pizza, (12 Medium) Cheese ,$12.50,Pizza,Large\n"+
		",,,,\n"+
		"Desi,Biryani,,,\n")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (blank line skipped)", len(rows))
	}
	r := rows[0]
	if r.Menu != DefaultImportMenu || r.Category != "Pizza" || r.Item != "(12 Medium) Cheese" || r.PriceCents != 1250 || !r.HasPrice || r.Printer != "Pizza" || r.Line != 2 {
		t.Errorf("row 0 = %+v", r)
	}
	if rows[1].HasPrice || rows[1].HasDescription || rows[1].Line != 4 {
		t.Errorf("row 1 = %+v", rows[1])
	}

	for name, in := range map[string]string{
		"missing column": "category,price\nPizza,1\n",
		"bad price":      "category,item,price\nPizza,Cheese,abc\n",
		"no item":        "category,item\nPizza,\n",
		"no rows":        "category,item\n",
		"empty":          "",
	} {
		if _, err := ParseMenuCSV(strings.NewReader(in)); !IsCSVError(err) {
			t.Errorf("%s: err = %v, want a CSVError", name, err)
		}
	}
}

const lombardis = `menu,category,item,price,printer
Main,Pizza,Cheese Pizza,10.00,Pizza
Main,Pizza,Pepperoni Pizza,12.00,Pizza
Main,Pizza,Garlic Knots,4.00,Sub
Main,Desi,Biryani,11.00,
Main,Desi,biryani ,11.00,
`

func TestImportCSV(t *testing.T) {
	ctx := context.Background()
	svc, repo, p := newImportSvc(t, "Pizza", "Sub", "Online")
	tid, lid := uuid.New(), uuid.New()

	// Dry run: reports, writes nothing.
	dry, err := svc.ImportCSV(ctx, tid, lid, mustParse(t, lombardis), true, false)
	if err != nil {
		t.Fatal(err)
	}
	if repo.calls != 0 || dry.Menus != 1 || dry.Categories != 2 || dry.Items != 4 || dry.ItemPrinters != 1 {
		t.Fatalf("dry run = %+v, writes %d", dry, repo.calls)
	}

	res, err := svc.ImportCSV(ctx, tid, lid, mustParse(t, lombardis), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Menus != 1 || res.Categories != 2 || res.Items != 4 || res.ItemPrinters != 1 {
		t.Fatalf("import = %+v", res)
	}
	if len(repo.items) != 4 { // "biryani " merged into "Biryani"
		t.Fatalf("items = %d, want 4", len(repo.items))
	}
	var pizzaCat *menuDom.Category
	for _, c := range repo.cats {
		if c.Name == "Pizza" {
			pizzaCat = c
		}
	}
	if pizzaCat.DefaultPrinterID == nil || *pizzaCat.DefaultPrinterID != p["Pizza"] {
		t.Errorf("pizza default printer = %v, want Pizza", pizzaCat.DefaultPrinterID)
	}
	for _, it := range repo.items {
		switch it.Name {
		case "Garlic Knots":
			if it.PrinterID == nil || *it.PrinterID != p["Sub"] {
				t.Errorf("garlic knots printer = %v, want Sub", it.PrinterID)
			}
		default:
			if it.PrinterID != nil {
				t.Errorf("%s has its own printer, want category default", it.Name)
			}
		}
	}

	// Idempotent.
	again, err := svc.ImportCSV(ctx, tid, lid, mustParse(t, lombardis), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Errorf("re-import changed: %v", again.Changes)
	}

	// Export → import is a no-op; the printer column is the effective printer.
	var buf bytes.Buffer
	if err := svc.ExportCSV(ctx, lid, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Main,Pizza,Cheese Pizza,10.00,Pizza,\n") ||
		!strings.Contains(buf.String(), "Main,Pizza,Garlic Knots,4.00,Sub,\n") {
		t.Errorf("export =\n%s", buf.String())
	}
	rt, err := svc.ImportCSV(ctx, tid, lid, mustParse(t, buf.String()), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.Changes) != 0 {
		t.Errorf("round trip changed: %v", rt.Changes)
	}

	// Price + printer edits; blank printer leaves Garlic Knots alone; naming
	// the category default clears an item's own printer.
	edit := "category,item,price,printer\nPizza,Cheese Pizza,10.50,\nPizza,Garlic Knots,,\nDesi,Biryani,,Online\n"
	ed, err := svc.ImportCSV(ctx, tid, lid, mustParse(t, edit), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if ed.ItemsUpdated != 1 || ed.CategoryPrinters != 1 || ed.ItemPrinters != 0 || ed.Items != 0 {
		t.Errorf("edit = %+v", ed)
	}

	// Unknown printer: nothing written.
	before := repo.calls
	_, err = svc.ImportCSV(ctx, tid, lid, mustParse(t, "category,item,printer\nPizza,Cheese Pizza,Grill\n"), false, false)
	if !IsCSVError(err) || !strings.Contains(err.Error(), "Grill") || repo.calls != before {
		t.Errorf("unknown printer: err = %v, writes %d", err, repo.calls-before)
	}

	// Replace wipes the location's menus first.
	rep, err := svc.ImportCSV(ctx, tid, lid, mustParse(t, "category,item\nSides,Fries\n"), false, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.MenusDeleted != 1 || len(repo.menus) != 1 || len(repo.items) != 1 || repo.items[0].Name != "Fries" {
		t.Errorf("replace = %+v, menus %d, items %d", rep, len(repo.menus), len(repo.items))
	}
}
