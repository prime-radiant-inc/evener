import { describe, expect, test } from "vitest";
import type {
	DraftHandoffEdge,
	DraftHandoffEndpoint,
	DraftHandoffGraph,
	DraftSubmissionObligation,
} from "./draftHandoffLedger";
import { type HandoffGraphLoad, orderHandoffGraph, readHandoffGraph } from "./draftHandoffGraph";

const endpoint = (sessionRef: string): DraftHandoffEndpoint => ({ hubId: "hub", sessionRef });

function edge(
	edgeId: string,
	from: string,
	to: string,
	options: { dependencies?: string[]; obligations?: string[]; createdAt?: number } = {},
): DraftHandoffEdge {
	return {
		edgeId,
		chainId: "chain",
		from: endpoint(from),
		to: endpoint(to),
		state: "pending",
		dependencies: options.dependencies ?? [],
		obligations: options.obligations ?? [],
		claimAt: null,
		deliveredHash: null,
		supersededBy: null,
		createdAt: options.createdAt ?? 0,
		stateChangedAt: options.createdAt ?? 0,
	};
}

function obligation(obligationId: string, state: DraftSubmissionObligation["state"]): DraftSubmissionObligation {
	return {
		obligationId,
		chainId: "chain",
		origin: endpoint("a"),
		state,
		completedCommandIds: [],
		createdAt: 0,
		resolvedAt: state === "open" ? null : 1,
	};
}

function load(edges: DraftHandoffEdge[], obligations: DraftSubmissionObligation[] = []): HandoffGraphLoad {
	const graph: DraftHandoffGraph = { edges, obligations, deliveries: [] };
	return { state: "ready", graph };
}

describe("handoff graph ordering", () => {
	test("settles a chain downstream first", () => {
		const ordering = orderHandoffGraph(load([edge("ab", "a", "b", { dependencies: ["bc"] }), edge("bc", "b", "c")]));
		expect(ordering).toEqual({
			state: "ready",
			order: [expect.objectContaining({ edgeId: "bc" }), expect.objectContaining({ edgeId: "ab" })],
			blocked: [],
		});
	});

	test("orders a shortcut by the full dependency graph, not its own timestamp", () => {
		const ordering = orderHandoffGraph(
			load([
				edge("ab", "a", "b", { dependencies: ["ac"], createdAt: 1 }),
				edge("ac", "a", "c", { dependencies: ["bc"], createdAt: 2 }),
				edge("bc", "b", "c", { createdAt: 3 }),
			]),
		);
		expect(ordering.state).toBe("ready");
		if (ordering.state !== "ready") return;
		expect(ordering.order.map((entry) => entry.edgeId)).toEqual(["bc", "ac", "ab"]);
	});

	test("breaks ties deterministically by creation time then edge id", () => {
		const source = load([edge("edge-b", "p", "q", { createdAt: 5 }), edge("edge-a", "x", "y", { createdAt: 5 })]);
		const first = orderHandoffGraph(source);
		const second = orderHandoffGraph(source);
		expect(first.state).toBe("ready");
		if (first.state !== "ready" || second.state !== "ready") return;
		expect(first.order.map((entry) => entry.edgeId)).toEqual(["edge-a", "edge-b"]);
		expect(second.order.map((entry) => entry.edgeId)).toEqual(first.order.map((entry) => entry.edgeId));
	});

	test("blocks an open obligation and everything behind it", () => {
		const ordering = orderHandoffGraph(
			load(
				[
					edge("xy", "x", "y", { obligations: ["o1"], createdAt: 1 }),
					edge("wx", "w", "x", { dependencies: ["xy"], createdAt: 2 }),
				],
				[obligation("o1", "open")],
			),
		);
		expect(ordering).toEqual({
			state: "ready",
			order: [],
			blocked: [
				{ edgeId: "xy", reason: "obligation" },
				{ edgeId: "wx", reason: "dependency" },
			],
		});
	});

	test("lets a resolved obligation settle", () => {
		const ordering = orderHandoffGraph(
			load([edge("xy", "x", "y", { obligations: ["o1"] })], [obligation("o1", "completed")]),
		);
		expect(ordering).toMatchObject({ state: "ready", order: [{ edgeId: "xy" }], blocked: [] });
	});

	test("reports a dependency naming no edge as invalid", () => {
		const ordering = orderHandoffGraph(load([edge("ab", "a", "b", { dependencies: ["missing"] })]));
		expect(ordering).toEqual({
			state: "invalid",
			invalid: [{ edgeId: "ab", reason: "missing-dependency" }],
		});
	});

	test("reports a cycle as invalid instead of ordering it", () => {
		const ordering = orderHandoffGraph(
			load([edge("ab", "a", "b", { dependencies: ["bc"] }), edge("bc", "b", "c", { dependencies: ["ab"] })]),
		);
		expect(ordering.state).toBe("invalid");
		if (ordering.state !== "invalid") return;
		expect(ordering.invalid.map((entry) => entry.edgeId).sort()).toEqual(["ab", "bc"]);
		expect(ordering.invalid.every((entry) => entry.reason === "cycle")).toBe(true);
	});

	test("fails closed when the graph read is incomplete", () => {
		const failing = {
			readGraph: () => {
				throw new Error("store unavailable");
			},
		};
		const incomplete = readHandoffGraph(failing);
		expect(incomplete).toEqual({ state: "incomplete" });
		expect(orderHandoffGraph(incomplete)).toEqual({ state: "incomplete" });
	});

	test("reloads the same graph idempotently", () => {
		const graph = load([edge("bc", "b", "c"), edge("ab", "a", "b", { dependencies: ["bc"], createdAt: 1 })]);
		const ledger = {
			readGraph: () => (graph.state === "ready" ? graph.graph : { edges: [], obligations: [], deliveries: [] }),
		};
		const first = orderHandoffGraph(readHandoffGraph(ledger));
		const second = orderHandoffGraph(readHandoffGraph(ledger));
		expect(first.state).toBe("ready");
		if (first.state !== "ready" || second.state !== "ready") return;
		expect(second.order.map((entry) => entry.edgeId)).toEqual(first.order.map((entry) => entry.edgeId));
		expect(second.blocked).toEqual(first.blocked);
	});
});
