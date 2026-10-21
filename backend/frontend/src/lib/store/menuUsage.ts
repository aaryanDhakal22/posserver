import { storeApi, type Category, type Item, type Menu } from '$lib/api';

export interface PrinterUsage {
  /** Categories whose default printer is this one. */
  cats: number;
  /** Items assigned to this printer directly. */
  items: number;
  /** Items that end up printing here (own printer, else their category's). */
  effective: number;
}

/**
 * How the menu uses each printer, read through the existing menu routes (one
 * walk of menus → categories → items). null if the menu couldn't be read.
 */
export async function menuUsage(): Promise<Record<string, PrinterUsage> | null> {
  try {
    const menus = await storeApi<Menu[]>('/me/menu/menus');
    const cats = (await Promise.all(menus.map((m) => storeApi<Category[]>(`/me/menu/menus/${m.id}/categories`)))).flat();
    const itemsByCat = await Promise.all(cats.map((c) => storeApi<Item[]>(`/me/menu/categories/${c.id}/items`)));
    const out: Record<string, PrinterUsage> = {};
    const at = (id: string) => (out[id] ??= { cats: 0, items: 0, effective: 0 });
    cats.forEach((c, i) => {
      if (c.default_printer_id) at(c.default_printer_id).cats++;
      for (const it of itemsByCat[i]) {
        if (it.printer_id) at(it.printer_id).items++;
        const eff = it.printer_id ?? c.default_printer_id;
        if (eff) at(eff).effective++;
      }
    });
    return out;
  } catch {
    return null;
  }
}
