import { displayedFingerprint, type AcceptedSourceLease, type CapturedEvidence } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { captureNativePassage, type NativeDocumentSelection } from "./nativeSelection";

const SOURCE = "First occurrence: same\n\nOther context for a second occurrence is nearby: same\n";
const RICH_SOURCE =
	"# Note\r\n\r\nfirst **same** [link](https://example.test) &amp; 😀\r\n\r\n" +
	"second same\r\n\r\n- item **same**\r\n\r\n" +
	"| Cell | Other |\r\n| --- | --- |\r\n| same | tail |\r\n\r\n```ts\r\nsame()\r\n```\r\n";
const lease: AcceptedSourceLease = {
	key: { namespace: { kind: "native", hubId: "hub-A" }, sessionRef: "session-A", path: "NOTES.md" },
	namespaceIncarnation: "incarnation-A",
	epoch: "epoch-A",
	headRevision: 7,
	displayedSnapshotId: "snapshot-A",
};
function evidence(source = SOURCE, truncated = false): CapturedEvidence {
	return { wholeRevision: "rev-A", fingerprint: displayedFingerprint(source, truncated) };
}
function event(overrides: Partial<NativeDocumentSelection> = {}): NativeDocumentSelection {
	return {
		displayedSnapshotId: "snapshot-A",
		mappingId: "native-map-A",
		sourceRange: { start: 18, end: 22 },
		selectedRenderedText: "same",
		phase: "completed",
		...overrides,
	};
}

describe("captureNativePassage", () => {
	it("captures native original coordinates and shared context, not the repeated rendered word", () => {
		expect(captureNativePassage(event(), SOURCE, lease, evidence())).toEqual({
			evidence: evidence(),
			sourceRange: { start: 18, end: 22 },
			start: { line: 1, column: 19 },
			endInclusive: { line: 1, column: 22 },
			selectedRenderedText: "same",
			contextBefore: "First occurrence: ",
			contextAfter: "",
			mappingId: "native-map-A",
			displayedSnapshotId: "snapshot-A",
		});
	});
	it.each([
		[18, 22, 3, 9, 3, 12],
		[73, 77, 5, 8, 5, 11],
		[90, 94, 7, 10, 7, 13],
		[135, 139, 11, 3, 11, 6],
		[159, 163, 14, 1, 14, 4],
		[18, 77, 3, 9, 5, 11],
	])(
		"keeps literal CRLF source [%i,%i), including table, code and cross-row coordinates",
		(start, end, sl, sc, el, ec) => {
			const capture = captureNativePassage(
				event({ sourceRange: { start, end } }),
				RICH_SOURCE,
				lease,
				evidence(RICH_SOURCE),
			);
			expect(capture.sourceRange).toEqual({ start, end });
			expect(capture.start).toEqual({ line: sl, column: sc });
			expect(capture.endInclusive).toEqual({ line: el, column: ec });
		},
	);
	it("keeps decoded selection text separate from the entity spelling", () => {
		const capture = captureNativePassage(
			event({ sourceRange: { start: 54, end: 59 }, selectedRenderedText: "&" }),
			RICH_SOURCE,
			lease,
			evidence(RICH_SOURCE),
		);
		expect(capture.selectedRenderedText).toBe("&");
		expect(capture.sourceRange).toEqual({ start: 54, end: 59 });
	});
	it("retains prefix evidence without inventing whole-file coverage", () => {
		const proof = { fingerprint: displayedFingerprint(SOURCE, true) };
		const capture = captureNativePassage(event(), SOURCE, lease, proof);
		expect(capture.evidence).toEqual(proof);
		expect(capture.evidence.fingerprint.scope).toBe("displayed-prefix");
		expect(capture.evidence.wholeRevision).toBeUndefined();
	});
	it("copies event and evidence records so future native updates cannot move a captured anchor", () => {
		const selection = event();
		const proof = evidence();
		const capture = captureNativePassage(selection, SOURCE, lease, proof);
		selection.sourceRange.start = 73;
		proof.fingerprint.coverage.end = 1;
		proof.wholeRevision = "rev-B";
		expect(capture.sourceRange).toEqual({ start: 18, end: 22 });
		expect(capture.evidence).toEqual(evidence());
	});
	it.each(["changing", "cleared"] as const)("does not capture a %s gesture", (phase) => {
		expect(() => captureNativePassage(event({ phase }), SOURCE, lease, evidence())).toThrow(/completed/);
	});
	it("rejects a recycled row's snapshot", () => {
		expect(() =>
			captureNativePassage(event({ displayedSnapshotId: "old-snapshot" }), SOURCE, lease, evidence()),
		).toThrow(/snapshot/);
	});
	it.each([{ mappingId: "" }, { selectedRenderedText: "" }])(
		"rejects missing native mapping or rendered selection: %j",
		(missing) => {
			expect(() => captureNativePassage(event(missing), SOURCE, lease, evidence())).toThrow(/mapped passage/);
		},
	);
	it.each([
		{ start: -1, end: 22 },
		{ start: 18, end: 18 },
		{ start: 22, end: 18 },
		{ start: 18, end: 173 },
		{ start: 18.5, end: 22 },
		{ start: 60, end: 61 },
		{ start: 61, end: 62 },
	])("uses shared UTF-16 validation for invalid source range %j", (sourceRange) => {
		expect(() => captureNativePassage(event({ sourceRange }), RICH_SOURCE, lease, evidence(RICH_SOURCE))).toThrow(
			RangeError,
		);
	});
});
