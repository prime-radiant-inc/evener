import assert from "node:assert/strict";
import { test } from "node:test";

import { frontendRootFrom, rawControlsIn, widgetConceptClassesIn } from "./ds-audit.mjs";

test("rawControlsIn counts native controls a widget should own", () => {
  const tsx = `
		<button type="button" onClick={go}>Go</button>
		<input value={v} />
		<select value={s}><option /></select>
		<textarea />
		<Button>Fine</Button>
		<buttonGroup />
	`;
  assert.deepEqual(rawControlsIn(tsx), { button: 1, input: 1, select: 1, textarea: 1 });
});

test("rawControlsIn ignores controls named only in comments", () => {
  assert.deepEqual(rawControlsIn("// a raw <button> here would be wrong\n{/* <input> */}"), {
    button: 0,
    input: 0,
    select: 0,
    textarea: 0,
  });
});

test("rawControlsIn still sees code after a line comment that mentions a glob", () => {
  assert.equal(rawControlsIn("// lives in widgets/**\n<input value={v} />\n// end */").input, 1);
});

test("widgetConceptClassesIn finds module classes that re-implement widgets", () => {
  const css = `
		.chip { display: inline-flex; }
		.pendingChip, .statusPill { gap: 4px; }
		.emptyHint:hover { color: red; }
		.row { display: flex; }
		.chip .label { color: blue; }
	`;
  assert.deepEqual(widgetConceptClassesIn(css), ["chip", "pendingChip", "statusPill", "emptyHint"]);
});

test("the frontend root is a real filesystem path even when the checkout path has spaces", () => {
  assert.equal(
    frontendRootFrom("file:///tmp/my%20checkout/frontend/scripts/ds-audit.mjs"),
    "/tmp/my checkout/frontend/",
  );
});

test("comment markers inside string literals do not hide code", () => {
  const tsx = '<a href="https://example.com" /><button />\n<input accept="image/*" /><select />';
  assert.deepEqual(rawControlsIn(tsx), { button: 1, input: 1, select: 1, textarea: 0 });
});
