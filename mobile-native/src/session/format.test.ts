import { expect, it } from "vitest";
import { compactCount, compactDuration } from "./format";

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
])("a duration of %i ms reads %s", (ms, text) => {
	expect(compactDuration(ms)).toBe(text);
});

it.each([
	[0, "0"],
	[999, "999"],
	[1_000, "1K"],
	[1_234, "1.2K"],
	[39_800, "39.8K"],
	[99_950, "100K"],
	[412_000, "412K"],
	[999_950, "1M"],
	[1_200_000, "1.2M"],
	[46_000_000, "46M"],
	[-3, "0"],
	[Number.NaN, "0"],
])("a count of %d reads %s", (n, text) => {
	expect(compactCount(n)).toBe(text);
});
