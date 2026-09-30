// The reading-position restore in ConversationScreen used to bail when
// `readerHeader.current` was true: an explicit action that revealed the
// transcript's header (it scrolled the list to offset 0) was not a semantic
// reader position, so the saved reading anchor must not pull the list back
// down. The action that set the ref ("Check delivery" / "Review error" /
// "Connection") became the recovery panel, which scrolls nothing, so the
// assignment to true went with it. What stayed behind in screens.tsx was a
// ref nothing ever sets true, its guard, and four resets that reset nothing
// (issue #3433).
//
// A ref read as a guard is dead code unless something writes it true. This
// pins that the screen carries no `readerHeader` - the name itself, so the
// declaration, the guard and every reset are caught together. A real one
// would come back with the writer that justifies it.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

// Assembled rather than spelled, so this guard never matches its own source.
const deadRef = ["reader", "Header"].join("");
const screens = fileURLToPath(new URL("./screens.tsx", import.meta.url));

it("screens.tsx carries no dead readerHeader ref", () => {
	const source = readFileSync(screens, "utf8");
	expect(source).not.toMatch(new RegExp(`\\b${deadRef}\\b`));
});
