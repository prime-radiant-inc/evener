import { expect, it } from "vitest";
import { spokenDuration } from "@evener/appwire-client";
import { timeAgo } from "./format";

it.each([
	[-5_000, "just now"],
	[0, "just now"],
	[59_999, "just now"],
	[60_000, "1m ago"],
	[3 * 3_600_000, "3h ago"],
])("%i ms ago reads %s", (ms, text) => {
	expect(timeAgo(ms)).toBe(text);
});

it("reads how long ago in the words it is given", () => {
	expect(timeAgo(3 * 60_000, spokenDuration)).toBe("3 minutes ago");
	// Under a minute there is no duration to word.
	expect(timeAgo(30_000, spokenDuration)).toBe(timeAgo(30_000));
});
