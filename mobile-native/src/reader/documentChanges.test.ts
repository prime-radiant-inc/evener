import { describe, expect, it } from "vitest";
import { documentBlocks } from "./documentBlocks";
import { anchorBlock, changedBlocks, changesCaption, restoreBlock, whenYouRead } from "./documentChanges";

const v1 = documentBlocks("# Plan\n\nFirst paragraph.\n\nSecond paragraph.\n\nThird paragraph.");
const v2 = documentBlocks(
	"# Plan\n\nA new opening.\n\nFirst paragraph.\n\nSecond paragraph, edited.\n\nThird paragraph.",
);
const hashes = (blocks: typeof v1) => blocks.map((block) => block.hash);

describe("changes since you last read (spec 10.2, ruling 13)", () => {
	it("marks the blocks whose words weren't in the version you read", () => {
		expect(changedBlocks(v2, hashes(v1))).toEqual([1, 3]);
	});

	it("marks nothing on a first read, or when nothing changed", () => {
		expect(changedBlocks(v2, null)).toEqual([]);
		expect(changedBlocks(v1, hashes(v1))).toEqual([]);
	});

	it("counts a repeated paragraph once per copy you read", () => {
		const once = documentBlocks("Same.\n\nOther.");
		const twice = documentBlocks("Same.\n\nOther.\n\nSame.");
		expect(changedBlocks(twice, hashes(once))).toEqual([2]);
	});

	it("says when you read it by this phone's calendar", () => {
		const now = new Date(2026, 8, 26, 9, 30).getTime();
		const readOn = (day: number, hour: number, minute = 0) =>
			whenYouRead(new Date(2026, 8, day, hour, minute).getTime(), now);
		const today = readOn(26, 0, 5);
		// Any time today is today, even one ahead of the phone's clock.
		expect(readOn(26, 11)).toBe(today);
		const yesterday = readOn(25, 23, 50);
		// Yesterday reads the same whatever day it was.
		expect(whenYouRead(new Date(2026, 8, 22, 23, 50).getTime(), new Date(2026, 8, 23, 9, 30).getTime())).toBe(
			yesterday,
		);
		const thisWeek = readOn(23, 12);
		const earlier = readOn(18, 12);
		expect(new Set([today, yesterday, thisWeek, earlier]).size).toBe(4);
		// Within the week it names the weekday, so another day reads differently;
		// from a week back it names the date.
		expect(readOn(22, 12)).not.toBe(thisWeek);
		expect(readOn(20, 12)).not.toMatch(/\d/);
		expect(readOn(19, 12)).toContain("19");
		expect(earlier).toContain("18");
		const readYesterday = new Date(2026, 8, 25, 18, 0).getTime();
		const three = changesCaption(3, readYesterday, now);
		const one = changesCaption(1, readYesterday, now);
		expect(three).toContain("3");
		expect(three?.endsWith(whenYouRead(readYesterday, now))).toBe(true);
		// One change reads in the singular.
		expect(one?.replace("1", "#")).not.toBe(three?.replace("3", "#"));
		expect(changesCaption(0, readYesterday, now)).toBeNull();
	});
});

describe("anchors in a document that changed (ruling 14, Review Focus 3)", () => {
	it("follows a paragraph that moved, to the copy nearest where it was", () => {
		const second = v1[2];
		expect(second?.text).toBe("Second paragraph.");
		const moved = documentBlocks("# Plan\n\nIntro.\n\nFirst paragraph.\n\nSecond paragraph.\n\nThird paragraph.");
		expect(anchorBlock({ blockHash: second?.hash ?? "", blockIndex: 2 }, moved)).toBe(3);
		const repeated = documentBlocks("Second paragraph.\n\nx\n\ny\n\nSecond paragraph.");
		expect(anchorBlock({ blockHash: second?.hash ?? "", blockIndex: 2 }, repeated)).toBe(3);
	});

	it("has no anchor once the paragraph's words changed", () => {
		expect(anchorBlock({ blockHash: v1[2]?.hash ?? "", blockIndex: 2 }, v2)).toBeNull();
	});

	it("restores a reading position to its block, the same words if they moved, else the nearest place", () => {
		const second = v1[2];
		const position = { blockIndex: 2, blockHash: second?.hash ?? "", offset: 40 };
		expect(restoreBlock(position, v1)).toEqual({ index: 2, offset: 40 });
		const moved = documentBlocks("# Plan\n\nIntro.\n\nFirst paragraph.\n\nSecond paragraph.");
		expect(restoreBlock(position, moved)).toEqual({ index: 3, offset: 40 });
		expect(restoreBlock({ ...position, blockIndex: 9 }, v2)).toEqual({ index: 4, offset: 0 });
		expect(restoreBlock(position, [])).toBeNull();
	});
});
