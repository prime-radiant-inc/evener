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
		expect(whenYouRead(new Date(2026, 8, 26, 0, 5).getTime(), now)).toBe("earlier today");
		expect(whenYouRead(new Date(2026, 8, 25, 23, 50).getTime(), now)).toBe("yesterday");
		expect(whenYouRead(new Date(2026, 8, 23, 12, 0).getTime(), now)).toBe("on Wednesday");
		expect(whenYouRead(new Date(2026, 8, 18, 12, 0).getTime(), now)).toBe("on Sep 18");
		expect(whenYouRead(new Date(2026, 8, 26, 11, 0).getTime(), now)).toBe("earlier today");
		expect(changesCaption(3, new Date(2026, 8, 25, 18, 0).getTime(), now)).toBe(
			"3 changes since you read it yesterday",
		);
		expect(changesCaption(1, new Date(2026, 8, 25, 18, 0).getTime(), now)).toBe("1 change since you read it yesterday");
		expect(changesCaption(0, new Date(2026, 8, 25, 18, 0).getTime(), now)).toBeNull();
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
