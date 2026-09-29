import { expect, it } from "vitest";
import { compactDuration, spokenDuration } from "./format";

it.each([
	[0, "0s"],
	[999, "0s"],
	[40_000, "40s"],
	[59_999, "59s"],
	[60_000, "1m"],
	[12 * 60_000, "12m"],
	[3_600_000, "1h"],
	[3 * 3_600_000 + 59 * 60_000, "3h"],
	[86_400_000, "1d"],
	[2 * 86_400_000 + 5, "2d"],
	[-5_000, "0s"],
	[Number.NaN, "0s"],
	[Number.POSITIVE_INFINITY, "0s"],
])("a duration of %i ms reads %s", (ms, text) => {
	expect(compactDuration(ms)).toBe(text);
});

// What VoiceOver reads for a compact duration: the same unit, in words.
it.each([
	[1_000, "1 second"],
	[40_000, "40 seconds"],
	[60_000, "1 minute"],
	[3 * 60_000, "3 minutes"],
	[3_600_000, "1 hour"],
	[3 * 3_600_000, "3 hours"],
	[86_400_000, "1 day"],
	[2 * 86_400_000, "2 days"],
	[Number.NaN, "0 seconds"],
])("reads %i ms as %s", (ms, text) => {
	expect(spokenDuration(ms)).toBe(text);
});
