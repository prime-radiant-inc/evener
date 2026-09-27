import { expect, it, vi } from "vitest";
import { DISCARD_TITLE, discardAlert, sheetLeave } from "./sheetLeave";

it.each([
	[false, false, "leave"],
	[false, true, "leave"],
	[true, false, "ask"],
	[true, true, "leave"],
] as const)("dirty %s, finishing %s: %s", (dirty, finishing, expected) => {
	expect(sheetLeave(dirty, finishing)).toBe(expected);
});

it("asks to keep editing first, and only Discard throws the input away", () => {
	const discard = vi.fn();
	const alert = discardAlert("Discard this comment?", discard);
	expect(alert.title).toBe("Discard this comment?");
	expect(alert.buttons.map(({ text, style }) => [text, style])).toEqual([
		["Keep editing", "cancel"],
		["Discard", "destructive"],
	]);
	alert.buttons[0]?.onPress?.();
	expect(discard).not.toHaveBeenCalled();
	alert.buttons[1]?.onPress?.();
	expect(discard).toHaveBeenCalledOnce();
	expect(DISCARD_TITLE).toBe("Discard your changes?");
});
