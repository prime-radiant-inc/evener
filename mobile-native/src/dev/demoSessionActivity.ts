import { isFailedJobOutcome, isPlainObject, WireError } from "@evener/appwire-client";
import type {
	ActivitySessionNode,
	ActivityTree,
	JobActivityJob,
	SessionActivityAncestor,
	SessionActivityContext,
	SessionActivityCounts,
	SessionActivityListParams,
	SessionActivityReadParams,
	SessionActivityScope,
	SessionDelegate,
	SessionWatch,
} from "@evener/appwire-client";

interface ActivityRoot {
	tree: ActivityTree;
	availability: "live" | "retained";
}

interface ActivityProjection {
	context: SessionActivityContext;
	scope: SessionActivityScope;
	delegates: SessionDelegate[];
	jobs: JobActivityJob[];
	watches: SessionWatch[];
}

/** Typed demo reads share the legacy fixture's activity authority, never navigation. */
export function createDemoSessionActivity(resolve: (ref: string) => ActivityRoot | null, epoch: string) {
	let sequence = 0;
	const continuations = new Map<string, unknown[]>();
	const invalid = () =>
		new WireError("Invalid demonstration activity request", -32602, { evenerErrorInfo: "invalidParams" });
	function cursorKey(sequence: number, ref: string, scope: SessionActivityScope, resource: string) {
		return JSON.stringify({ version: 1, sequence, ref, scope, resource });
	}
	function readCursor(cursor: string, ref: string, scope: SessionActivityScope, resource: string) {
		let token: unknown;
		try {
			token = JSON.parse(cursor);
		} catch {
			throw invalid();
		}
		// Binding survives FIFO eviction; the issuance range needs no tombstone history.
		if (
			!isPlainObject(token) ||
			Object.keys(token).length !== 5 ||
			token.version !== 1 ||
			typeof token.sequence !== "number" ||
			!Number.isSafeInteger(token.sequence) ||
			token.sequence <= 0 ||
			token.sequence > sequence ||
			token.ref !== ref ||
			token.scope !== scope ||
			token.resource !== resource
		)
			throw invalid();
		return cursorKey(token.sequence, ref, scope, resource);
	}
	function project(params: SessionActivityReadParams): ActivityProjection {
		const scope = params.scope ?? "session";
		if (!params.ref || (scope !== "session" && scope !== "subtree")) throw invalid();
		const source = resolve(params.ref);
		if (!source || !source.tree.root.sessionId)
			throw new WireError("Demonstration session not found", -32602, { evenerErrorInfo: "resourceNotFound" });
		const root = source.tree.root;
		const contexts = new Map<string, SessionActivityContext>();
		const parents = new Map<string, string>();
		const delegates: SessionDelegate[] = [];
		const jobs: JobActivityJob[] = [];
		const rootContext: SessionActivityContext = {
			ref: root.ref,
			sessionId: source.tree.root.sessionId,
			rootRef: root.ref,
			ancestors: [],
			ancestryKnown: true,
			epoch,
			availability: source.availability,
		};
		contexts.set(root.ref, rootContext);
		function visit(node: ActivitySessionNode, context: SessionActivityContext) {
			for (const entry of node.entries) {
				if (entry.kind === "shell") {
					jobs.push({ ...entry.job, transcriptRef: entry.job.ownerRef });
					continue;
				}
				const delegate = entry.delegate;
				if (!delegate.childSessionId) throw invalid();
				const terminal = delegate.terminal === true;
				const row: SessionDelegate = {
					runGeneration: delegate.runGeneration ?? 0,
					...(delegate.reportPreview !== undefined ? { reportPreview: delegate.reportPreview } : {}),
					...(delegate.reportPreviewTruncated !== undefined
						? { reportPreviewTruncated: delegate.reportPreviewTruncated }
						: {}),
					delegateId: delegate.delegateId,
					ownerRef: node.ref,
					rootRef: root.ref,
					childRef: delegate.childRef,
					...(context.delegateId ? { parentDelegateId: context.delegateId } : {}),
					description: delegate.description ?? "",
					task: delegate.task ?? "",
					type: "delegate",
					lifecycle: terminal ? "idle" : "running",
					phase: terminal ? "idle" : "running",
					status: terminal ? "idle" : "running",
					...(delegate.outcome ? { outcome: delegate.outcome } : {}),
					terminal,
					resumable: false,
					...(delegate.reason ? { reason: delegate.reason } : {}),
					...(delegate.model ? { model: delegate.model } : {}),
					...(delegate.runStartedAt ? { runStartedAt: delegate.runStartedAt } : {}),
					...(delegate.runEndedAt ? { runEndedAt: delegate.runEndedAt } : {}),
					...(delegate.latestActivityAt ? { latestActivityAt: delegate.latestActivityAt } : {}),
					...(delegate.usage ? { usage: delegate.usage } : {}),
					...(delegate.worktree ? { worktree: delegate.worktree } : {}),
				};
				delegates.push(row);
				parents.set(row.childRef, node.ref);
				const ancestor: SessionActivityAncestor = {
					ref: node.ref,
					sessionId: context.sessionId,
					title: node.label,
					...(context.delegateId ? { delegateId: context.delegateId } : {}),
				};
				const childContext: SessionActivityContext = {
					ref: row.childRef,
					sessionId: delegate.childSessionId,
					rootRef: root.ref,
					parentRef: node.ref,
					delegateId: row.delegateId,
					ancestors: [...context.ancestors, ancestor],
					ancestryKnown: true,
					epoch,
					availability: rootContext.availability === "retained" || terminal ? "retained" : "live",
				};
				contexts.set(row.childRef, childContext);
				if (delegate.child) visit(delegate.child, childContext);
			}
		}
		visit(root, rootContext);
		const context = contexts.get(params.ref);
		if (!context) throw invalid();
		function selected(owner: string) {
			if (scope === "session") return owner === params.ref;
			for (let ref: string | undefined = owner; ref; ref = parents.get(ref)) if (ref === params.ref) return true;
			return false;
		}
		return {
			context,
			scope,
			delegates: delegates.filter((row) => selected(row.ownerRef)),
			jobs: jobs.filter((row) => selected(row.ownerRef)),
			watches: [],
		};
	}
	function page<T>(params: SessionActivityListParams, resource: string, rows: T[]) {
		const scope = params.scope ?? "session";
		if (params.limit !== undefined && (!Number.isInteger(params.limit) || params.limit < 0)) throw invalid();
		const limit = Math.min(params.limit || 50, 200);
		if (params.cursor) {
			const held = continuations.get(readCursor(params.cursor, params.ref, scope, resource));
			if (!held)
				throw new WireError("Demonstration activity continuation expired", -32602, {
					evenerErrorInfo: "sessionActivityCursorStale",
				});
			rows = held as T[];
		}
		const admitted = rows.slice(0, limit);
		let nextCursor: string | undefined;
		if (admitted.length < rows.length) {
			nextCursor = cursorKey(++sequence, params.ref, scope, resource);
			continuations.set(nextCursor, rows.slice(admitted.length));
			if (continuations.size > 128) {
				const oldest = continuations.keys().next().value;
				if (oldest !== undefined) continuations.delete(oldest);
			}
		}
		return {
			rows: admitted,
			page: { complete: nextCursor === undefined, issues: [], ...(nextCursor ? { nextCursor } : {}) },
		};
	}
	const descending = (a: string, b: string) => (a === b ? 0 : a > b ? -1 : 1);
	function counts<T>(rows: T[], state: (row: T) => "active" | "failed" | "completed"): SessionActivityCounts {
		const counts = { known: true, total: rows.length, active: 0, failed: 0, completed: 0 };
		for (const row of rows) counts[state(row)]++;
		return counts;
	}
	return {
		summary(params: SessionActivityReadParams) {
			const { context, scope, delegates, jobs } = project(params);
			return {
				context,
				scope,
				delegates: counts(delegates, (row) =>
					!row.terminal ? "active" : row.outcome === "failed" ? "failed" : "completed",
				),
				jobs: counts(jobs, (row) =>
					!row.terminal ? "active" : isFailedJobOutcome(row.outcome) ? "failed" : "completed",
				),
				watches: counts([], () => "completed"),
			};
		},
		delegates(params: SessionActivityListParams) {
			const { context, scope, delegates } = project(params);
			delegates.sort(
				(a, b) => descending(a.runStartedAt ?? "", b.runStartedAt ?? "") || descending(a.delegateId, b.delegateId),
			);
			const result = page(params, "delegates", delegates);
			return { context, scope, page: result.page, delegates: result.rows };
		},
		jobs(params: SessionActivityListParams) {
			const { context, scope, jobs } = project(params);
			jobs.sort((a, b) => descending(a.startedAt, b.startedAt) || descending(a.jobId, b.jobId));
			const result = page(params, "jobs", jobs);
			return { context, scope, page: result.page, jobs: result.rows };
		},
		watches(params: SessionActivityListParams) {
			const { context, scope, watches } = project(params);
			const result = page(params, "watches", watches);
			return { context, scope, page: result.page, watches: result.rows };
		},
	};
}
