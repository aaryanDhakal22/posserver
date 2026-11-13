package bankimport

import (
	"context"
	"testing"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

// The store's Quick add item (00008) is main's own: the export leaves it (and
// its now-empty category / menu) out, a pruning apply keeps all three.
func TestQuickAddNotExportedNorPruned(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	svc := testService(f)
	if _, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, renameDoc(), Options{}); err != nil {
		t.Fatal(err)
	}
	m := menu.Menu{ID: f.id(), Name: "Quick add", Available: true, Position: 1000}
	c := menu.Category{ID: f.id(), MenuID: m.ID, Name: "Quick add", Available: true}
	it := menu.BankItem{Item: menu.Item{ID: f.id(), CategoryID: c.ID, Name: "Quick add", InStock: true, QuickAdd: true}}
	f.menus, f.cats, f.items = append(f.menus, m), append(f.cats, c), append(f.items, it)

	doc, err := svc.Export(ctx, uuid.Nil, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, dm := range doc.Menus {
		if dm.Name == "Quick add" {
			t.Fatalf("exported the Quick add menu: %+v", dm)
		}
	}
	if len(doc.Menus) != 1 || len(doc.Menus[0].Categories[0].Items) != 2 {
		t.Fatalf("menus: %+v", doc.Menus)
	}
	res, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, doc, Options{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range res.Changes {
		t.Errorf("a re-apply changed something: %s", ch)
	}
	if f.item(it.ID) == nil || len(f.menus) != 2 || len(f.cats) != 2 {
		t.Fatalf("pruned: menus %d cats %d item %v", len(f.menus), len(f.cats), f.item(it.ID))
	}
}
