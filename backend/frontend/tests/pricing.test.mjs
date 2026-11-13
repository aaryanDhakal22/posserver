// The TypeScript pricing engine against main's shared cases: every case's
// full quote must equal the Go engine's recorded one (internal/domain/pricing
// /testdata/cases). Run: node --test "tests/*.test.mjs" (Node ≥ 23 runs .ts directly).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { price } from '../src/lib/pricing/engine.ts';

const dir = join(dirname(fileURLToPath(import.meta.url)), '../../internal/domain/pricing/testdata/cases');
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort();

test('there are shared cases', () => assert.ok(files.length >= 20, `only ${files.length}`));

for (const f of files) {
  const c = JSON.parse(readFileSync(join(dir, f), 'utf8'));
  test(c.name, () => {
    assert.ok(c.quote, 'no recorded quote: run the Go test with -update');
    const got = JSON.parse(JSON.stringify(price(c.context, c.cart, new Date(c.at))));
    assert.deepEqual(got, c.quote);
  });
}

// Lombardi's real coupons and prices (lombardis_test.go): every case's quote must equal the Go engine's.
const lombardis = JSON.parse(readFileSync(join(dir, '../lombardis_coupons.json'), 'utf8'));
test("Lombardi's coupons: there are cases", () => assert.ok(lombardis.cases.length >= 18, `only ${lombardis.cases.length}`));
for (const c of lombardis.cases) {
  test(`Lombardi's: ${c.name}`, () => {
    assert.ok(c.quote, 'no recorded quote: run the Go test with -update');
    const got = JSON.parse(JSON.stringify(price(lombardis.context, c.cart, new Date(lombardis.at))));
    assert.deepEqual(got, c.quote);
  });
}
