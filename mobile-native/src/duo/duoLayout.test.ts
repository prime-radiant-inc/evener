import { expect, it } from "vitest";
import { isDuoInner } from "./duoLayout";

it("keeps the Duo's outer display single-pane", () => {
	expect(isDuoInner(466, 678)).toBe(false);
	expect(isDuoInner(678, 466)).toBe(false);
});

it("splits on the Duo's inner display in either orientation", () => {
	expect(isDuoInner(669, 951)).toBe(true);
	expect(isDuoInner(951, 669)).toBe(true);
});

it("keeps a regular iPhone single-pane in either orientation", () => {
	expect(isDuoInner(393, 852)).toBe(false);
	expect(isDuoInner(852, 393)).toBe(false);
});
