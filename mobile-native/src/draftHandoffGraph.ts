import type {
	DraftHandoffEdge,
	DraftHandoffEndpoint,
	DraftHandoffGraph,
	DraftHandoffLedger,
} from "./draftHandoffLedger";
import type { DraftRecord } from "./draftRepository";

/** One read of the ledger. A failing read is an incomplete graph, never a
 * partial one: settlement must not be planned against edges it could not see. */
export type HandoffGraphLoad = { state: "ready"; graph: DraftHandoffGraph } | { state: "incomplete" };

export type HandoffBlockedReason = "obligation" | "dependency";
export type HandoffInvalidReason = "cycle" | "missing-dependency";

export type HandoffOrdering =
	| {
			state: "ready";
			/** Eligible pending edges, downstream first. */
			order: DraftHandoffEdge[];
			/** Pending edges that are not eligible yet, in the same deterministic order. */
			blocked: { edgeId: string; reason: HandoffBlockedReason }[];
	  }
	| { state: "invalid"; invalid: { edgeId: string; reason: HandoffInvalidReason }[] }
	| { state: "incomplete" };

/** Read the whole graph in one call. readGraph issues its queries synchronously
 * on the single owning connection; a throw means the store is unreadable, so no
 * ordering is produced. */
export function readHandoffGraph(ledger: { readGraph(): DraftHandoffGraph }): HandoffGraphLoad {
	try {
		return { state: "ready", graph: ledger.readGraph() };
	} catch {
		return { state: "incomplete" };
	}
}

/** One bounded read of the graph's outstanding work. A ledger without the
 * bounded read falls back to the whole graph. */
export function readOutstandingHandoffGraph(ledger: DraftHandoffLedger): HandoffGraphLoad {
	try {
		return { state: "ready", graph: ledger.readOutstandingGraph?.() ?? ledger.readGraph() };
	} catch {
		return { state: "incomplete" };
	}
}

/** Follow the newest live edge from an origin to the identity that currently
 * owns its composition. A visited set stops a malformed cycle from spinning. */
export function terminalDestination(
	edges: readonly DraftHandoffEdge[],
	origin: DraftHandoffEndpoint,
	chainId?: string,
): DraftHandoffEndpoint {
	const seen = new Set<string>();
	let current = origin;
	for (;;) {
		const key = `${current.hubId}\u0000${current.sessionRef}`;
		if (seen.has(key)) return current;
		seen.add(key);
		const outgoing = edges
			.filter(
				(edge) =>
					(edge.composedAt !== null || edge.resolvedAs === "settled") &&
					edge.resolvedAs !== "refused" &&
					(chainId === undefined || edge.chainId === chainId) &&
					sameEndpoint(edge.from, current),
			)
			.sort((a, b) =>
				a.createdAt === b.createdAt
					? a.edgeId < b.edgeId
						? -1
						: a.edgeId > b.edgeId
							? 1
							: 0
					: a.createdAt - b.createdAt,
			);
		const newest = outgoing[outgoing.length - 1];
		if (newest === undefined) return current;
		current = newest.to;
	}
}

function sameEndpoint(a: DraftHandoffEndpoint, b: DraftHandoffEndpoint): boolean {
	return a.hubId === b.hubId && a.sessionRef === b.sessionRef;
}

/** The identity that durably owns a resolved capture. The graph's terminal is
 * the answer unless one of the chain's hops durably holds the capture token: a
 * transfer can commit while its composed-marker write fails, and a delayed
 * predecessor can land its composition at an intermediate identity after the
 * continuation moved on, a submission begun at a reopened origin durably holds
 * its token there while historical edges would route it elsewhere, and a
 * converged chain's continuation can carry a capture through edges this
 * obligation's own chain never names. The capture token is unique, so durable
 * evidence at any identity the origin's handoffs reached - or at the origin
 * itself - wins over the graph's routing, and an obligation no edge ever
 * adopted is never routed through unrelated historical edges. */
export function captureTerminal(
	edges: readonly DraftHandoffEdge[],
	obligation: {
		obligationId: string;
		origin: DraftHandoffEndpoint;
		chainId: string | null;
	},
	read: (endpoint: DraftHandoffEndpoint) => DraftRecord | undefined,
): DraftHandoffEndpoint {
	// Walk every identity the origin's handoffs could have reached, in
	// deterministic order: converging chains can move a capture through a
	// continuation this obligation's own chain never names, so routing by chain
	// alone can miss it. Every read is gated on the exact token, so only the
	// record durably holding it can match.
	const ordered = [...edges].sort(compareEdges);
	const queue: DraftHandoffEndpoint[] = [obligation.origin];
	const seen = new Set([endpointKey(obligation.origin)]);
	for (let index = 0; index < queue.length; index++) {
		const current = queue[index];
		for (const edge of ordered) {
			if (!sameEndpoint(edge.from, current)) continue;
			if (seen.has(endpointKey(edge.to))) continue;
			seen.add(endpointKey(edge.to));
			queue.push(edge.to);
			const record = read(edge.to);
			if (record?.unconfirmedCaptureToken === obligation.obligationId) return edge.to;
		}
	}
	// The origin's own durable record can hold the exact token: a submission
	// begun after every historical handoff cleaned up never moved, and durable
	// evidence at the origin wins over graph routing.
	if (read(obligation.origin)?.unconfirmedCaptureToken === obligation.obligationId) return obligation.origin;
	// An obligation no edge ever adopted cannot have been routed by the graph:
	// its capture either sits at a hop checked above or at the origin, and
	// routing it through unrelated historical edges would strand it.
	if (obligation.chainId === null) return obligation.origin;
	return terminalDestination(edges, obligation.origin, obligation.chainId ?? undefined);
}

function endpointKey(endpoint: DraftHandoffEndpoint): string {
	return `${endpoint.hubId}\u0000${endpoint.sessionRef}`;
}

/** Deterministic settlement planning over the durable graph.
 *
 * - Order is a topological sort where an edge runs after every edge it depends
 *   on, so a chain's downstream edge settles before its predecessor.
 * - Ties break on (createdAt, edgeId), never on path length or map order.
 * - An edge is planned after the edges it depends on, so a chain's downstream
 *   edge is attempted first. An open obligation blocks its edge and every edge
 *   that depends on it.
 * - A cycle or a dependency naming no edge makes the graph invalid; callers must
 *   transfer nothing rather than guess an order.
 * - Re-running over the same graph returns the same ids.
 */
export function orderHandoffGraph(load: HandoffGraphLoad): HandoffOrdering {
	if (load.state === "incomplete") return { state: "incomplete" };
	const graph = load.graph;
	const byId = new Map(graph.edges.map((edge) => [edge.edgeId, edge]));
	const pending = graph.edges.filter((edge) => edge.state === "pending");
	const pendingIds = new Set(pending.map((edge) => edge.edgeId));
	const openObligations = new Set(
		graph.obligations.filter((obligation) => obligation.state === "open").map((obligation) => obligation.obligationId),
	);
	const knownObligations = new Set(graph.obligations.map((obligation) => obligation.obligationId));
	const invalid = new Map<string, HandoffInvalidReason>();

	for (const edge of pending) {
		for (const dependency of edge.dependencies) {
			if (!byId.has(dependency)) invalid.set(edge.edgeId, "missing-dependency");
		}
	}
	propagateInvalid(pending, invalid);

	// Cycle detection over pending dependencies only: a settled dependency is
	// satisfied and cannot participate in a live cycle.
	const colors = new Map<string, "visiting" | "done">();
	const stack: string[] = [];
	const visit = (edgeId: string): void => {
		if (invalid.has(edgeId)) return;
		const color = colors.get(edgeId);
		if (color === "done") return;
		if (color === "visiting") {
			const start = stack.indexOf(edgeId);
			for (const member of stack.slice(start)) invalid.set(member, "cycle");
			return;
		}
		colors.set(edgeId, "visiting");
		stack.push(edgeId);
		for (const dependency of byId.get(edgeId)?.dependencies ?? []) if (pendingIds.has(dependency)) visit(dependency);
		stack.pop();
		colors.set(edgeId, "done");
	};
	for (const edge of pending) visit(edge.edgeId);
	// A dependent of a cycle member is just as unordered as the cycle itself.
	propagateInvalid(pending, invalid);

	if (invalid.size > 0) {
		const ordered = [...invalid.entries()]
			.map(([edgeId, reason]) => ({ edge: byId.get(edgeId), reason }))
			.filter((entry): entry is { edge: DraftHandoffEdge; reason: HandoffInvalidReason } => entry.edge !== undefined)
			.sort((a, b) => compareEdges(a.edge, b.edge))
			.map(({ edge, reason }) => ({ edgeId: edge.edgeId, reason }));
		return { state: "invalid", invalid: ordered };
	}

	// Kahn's algorithm, choosing the smallest (createdAt, edgeId) ready node at
	// each step so the order never depends on query or map insertion order.
	const indegree = new Map<string, number>();
	const dependents = new Map<string, string[]>();
	for (const edge of pending) {
		const liveDependencies = edge.dependencies.filter((dependency) => pendingIds.has(dependency));
		indegree.set(edge.edgeId, liveDependencies.length);
		for (const dependency of liveDependencies) {
			const list = dependents.get(dependency) ?? [];
			list.push(edge.edgeId);
			dependents.set(dependency, list);
		}
	}
	const ready = pending.filter((edge) => (indegree.get(edge.edgeId) ?? 0) === 0).sort(compareEdges);
	const sorted: DraftHandoffEdge[] = [];
	while (ready.length > 0) {
		const edge = ready.shift();
		if (edge === undefined) break;
		sorted.push(edge);
		for (const dependentId of dependents.get(edge.edgeId) ?? []) {
			const remaining = (indegree.get(dependentId) ?? 0) - 1;
			indegree.set(dependentId, remaining);
			if (remaining !== 0) continue;
			const dependent = byId.get(dependentId);
			if (dependent !== undefined) {
				ready.push(dependent);
				ready.sort(compareEdges);
			}
		}
	}

	const order: DraftHandoffEdge[] = [];
	const blocked: { edgeId: string; reason: HandoffBlockedReason }[] = [];
	const blockedIds = new Map<string, HandoffBlockedReason>();
	for (const edge of sorted) {
		if (edge.obligations.some((obligation) => openObligations.has(obligation) || !knownObligations.has(obligation))) {
			blockedIds.set(edge.edgeId, "obligation");
			blocked.push({ edgeId: edge.edgeId, reason: "obligation" });
			continue;
		}
		if (edge.dependencies.some((dependency) => blockedIds.has(dependency))) {
			blockedIds.set(edge.edgeId, "dependency");
			blocked.push({ edgeId: edge.edgeId, reason: "dependency" });
			continue;
		}
		order.push(edge);
	}
	return { state: "ready", order, blocked };
}

export function compareEdges(a: DraftHandoffEdge, b: DraftHandoffEdge): number {
	if (a.createdAt !== b.createdAt) return a.createdAt - b.createdAt;
	return a.edgeId < b.edgeId ? -1 : a.edgeId > b.edgeId ? 1 : 0;
}

/** An edge inherits its dependency's invalidity, transitively. */
function propagateInvalid(pending: readonly DraftHandoffEdge[], invalid: Map<string, HandoffInvalidReason>): void {
	let propagated = true;
	while (propagated) {
		propagated = false;
		for (const edge of pending) {
			if (invalid.has(edge.edgeId)) continue;
			for (const dependency of edge.dependencies) {
				const reason = invalid.get(dependency);
				if (reason !== undefined) {
					invalid.set(edge.edgeId, reason);
					propagated = true;
					break;
				}
			}
		}
	}
}
