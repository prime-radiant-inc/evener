import { describe, expect, it } from "vitest";
import {
	checkedKeybindingChange,
	keybindingPreview,
	replaceKeybindingRule,
} from "./keybindingRules";

describe("native hub shortcut rules", () => {
	it("refuses newly introduced conflicts while preserving existing unknown rules", () => {
		const raw = [
			{ action: "future.action", chord: "Meta+U" },
			{ action: "palette.open", chord: null },
			{ action: "composer.focus", chord: "Meta+K" },
		];
		expect(() => checkedKeybindingChange(raw, "palette.open")).toThrow();
		expect(checkedKeybindingChange(raw, "composer.focus", "Meta+P")[0]).toEqual(
			raw[0],
		);
		expect(() =>
			checkedKeybindingChange([], "palette.open", "Meta+"),
		).toThrow();
	});
	it("preserves unknown rules and removes all overrides only for the edited action", () => {
		const future = { action: "future.action", chord: "Control+U" };
		const rules = [
			future,
			{ action: "composer.focus", chord: "Meta+P" },
			{ action: "composer.focus", chord: null },
		];
		expect(replaceKeybindingRule(rules, "composer.focus")).toEqual([future]);
		expect(replaceKeybindingRule(rules, "composer.focus", null)).toEqual([
			future,
			{ action: "composer.focus", chord: null },
		]);
		expect(replaceKeybindingRule(rules, "composer.focus", "Meta+L")).toEqual([
			future,
			{ action: "composer.focus", chord: "Meta+L" },
		]);
	});
	it("uses the web catalog and effective unbind and reset behavior", () => {
		const initial = keybindingPreview([]);
		const palette = initial.rows.find((row) => row.actionId === "palette.open");
		expect(palette?.shortcuts).toContain("$mod+K");
		const unbound = keybindingPreview([
			{ action: "palette.open", chord: null },
		]);
		expect(
			unbound.rows.find((row) => row.actionId === "palette.open")?.shortcuts,
		).toEqual([]);
		expect(unbound.warnings).toEqual([]);
	});
	it("resolves Mod for the selected platform when detecting reserved shortcuts", () => {
		const rules = [{ action: "palette.open", chord: "$mod+Q" }];
		expect(keybindingPreview(rules, "apple").warnings[0]?.reason).toBe(
			"reserved-chord",
		);
		expect(keybindingPreview(rules, "other").warnings).toEqual([]);
	});
	it("shares conflicts and malformed-chord detection with the web validator", () => {
		const invalid = keybindingPreview([
			{ action: "composer.focus", chord: "Meta+K" },
		]);
		expect(invalid.warnings[0]).toMatchObject({
			reason: "conflict",
			conflictWith: "palette.open",
		});
		expect(
			invalid.rows.find((row) => row.actionId === "composer.focus")?.shortcuts,
		).toEqual(["$mod+I"]);
		expect(
			keybindingPreview([{ action: "palette.open", chord: "" }]).warnings[0]
				?.reason,
		).toBe("unparseable-chord");
		const available = keybindingPreview([
			{ action: "palette.open", chord: null },
			{ action: "composer.focus", chord: "Meta+K" },
		]);
		expect(available.warnings).toEqual([]);
		expect(
			available.rows.find((row) => row.actionId === "composer.focus")
				?.shortcuts,
		).toEqual(["Meta+K"]);
	});
});
