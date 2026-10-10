import { describe, expect, it } from "vitest";
import { liveBands } from "../board/attention";
import type { LiveBands } from "../board/attention";
import { liveOrder, neighbor, nextNavigation, nextQueue, othersNeedingYou, servedFirst } from "./fleetOrder";
import { fleetSession as row } from "./fleetTestUtils";

const at = (minute: number) => new Date(Date.UTC(2026, 8, 26, 12, minute)).toISOString();
const failed = row("failed", { state: "errored", updated_at: at(5) });
const question = row("question", { state: "awaiting", ask_pending: true, updated_at: at(1) });
const working = row("working", { state: "active", updated_at: at(9) });
const idle = row("idle", { state: "idle", updated_at: at(8) });
const bands = liveBands([failed, question, working, idle], [failed, question], () => false);
/** Next's destination: the head of the order it serves. */
const nextSession = (from: LiveBands, ref: string, recent: readonly string[]) =>
	nextQueue(from, ref, recent)[0] ?? null;

describe("who else needs you (spec 13.2)", () => {
	it("counts every session that needs you except this one", () => {
		expect(othersNeedingYou(bands, "failed").map((r) => r.ref)).toEqual(["question"]);
		expect(othersNeedingYou(bands, "working").map((r) => r.ref)).toEqual(["failed", "question"]);
	});

	it("sends Next to the head of Needs you from elsewhere, and on to the one after from inside it (ruling 11)", () => {
		expect(nextSession(bands, "working", [])?.ref).toBe("failed");
		expect(nextSession(bands, "failed", [])?.ref).toBe("question");
		expect(
			nextSession(
				liveBands([working], [], () => false),
				"working",
				[],
			),
		).toBeNull();
	});

	it("walks on through everyone who needs you, and wraps around", () => {
		const a = row("a", { state: "errored", updated_at: at(1) });
		const b = row("b", { state: "errored", updated_at: at(2) });
		const c = row("c", { state: "errored", updated_at: at(3) });
		const three = liveBands([a, b, c], [a, b, c], () => false);
		expect(nextSession(three, "a", [])?.ref).toBe("b");
		expect(nextSession(three, "b", [])?.ref).toBe("c");
		expect(nextSession(three, "c", [])?.ref).toBe("a");
		expect(
			nextSession(
				liveBands([a], [a], () => false),
				"a",
				[],
			),
		).toBeNull();
	});
});

describe("Next serves what alerted you first (spec 8.3)", () => {
	const refs = (list: readonly { ref: string }[]) => list.map((entry) => entry.ref);

	it("puts whatever alerted most recently first, then keeps Needs you order", () => {
		const needsYou = [{ ref: "failed-old" }, { ref: "failed-new" }, { ref: "question" }, { ref: "approval" }];
		expect(refs(servedFirst(needsYou, ["approval", "question"]))).toEqual([
			"approval",
			"question",
			"failed-old",
			"failed-new",
		]);
	});

	it("ignores alerts about sessions that no longer need you, and changes nothing without alerts", () => {
		const needsYou = [{ ref: "a" }, { ref: "b" }];
		expect(refs(servedFirst(needsYou, ["gone", "b"]))).toEqual(["b", "a"]);
		expect(refs(servedFirst(needsYou, []))).toEqual(["a", "b"]);
	});

	it("sends Next to the session that alerted most recently, never the one on screen", () => {
		expect(nextSession(bands, "working", ["question"])?.ref).toBe("question");
		expect(nextSession(bands, "question", ["question"])?.ref).toBe("failed");
	});

	it("walks on from this session in Needs you order after what alerted you", () => {
		const a = row("a", { state: "errored", updated_at: at(1) });
		const b = row("b", { state: "errored", updated_at: at(2) });
		const c = row("c", { state: "errored", updated_at: at(3) });
		const d = row("d", { state: "errored", updated_at: at(4) });
		const four = liveBands([a, b, c, d], [a, b, c, d], () => false);
		expect(nextSession(four, "b", ["d"])?.ref).toBe("d");
		expect(nextSession(four, "c", [])?.ref).toBe("d");
		expect(nextSession(four, "d", [])?.ref).toBe("a");
		// Its touch-and-hold list reads the same order, Next's destination first.
		expect(nextQueue(four, "b", ["d"]).map((r) => r.ref)).toEqual(["d", "c", "a"]);
	});
});

describe("Live order for the title bar's swipes (spec 6)", () => {
	it("walks Needs you, Working and Idle", () => {
		expect(liveOrder(bands).map((r) => r.ref)).toEqual(["failed", "question", "working", "idle"]);
	});

	it("finds the neighbor either way, and nothing past the ends or off the list", () => {
		const order = liveOrder(bands);
		expect(neighbor(order, "question", 1)?.ref).toBe("working");
		expect(neighbor(order, "question", -1)?.ref).toBe("failed");
		expect(neighbor(order, "failed", -1)).toBeNull();
		expect(neighbor(order, "idle", 1)).toBeNull();
		expect(neighbor(order, "gone", 1)).toBeNull();
	});
});

it("pushes from where you started, and replaces from a session Next opened (spec 8.3)", () => {
	expect(nextNavigation(undefined)).toBe("push");
	expect(nextNavigation("next")).toBe("replace");
});
