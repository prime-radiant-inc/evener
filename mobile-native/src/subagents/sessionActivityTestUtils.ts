// Typed external activity replies for native tests that share the rich row fixtures.
import {
	type ActivityTree,
	type Thread,
	type SessionActivityContext,
	type SessionActivityCounts,
	type SessionActivityReadParams,
	type SessionDelegate,
	type SessionDelegatesResponse,
	type SessionJobsResponse,
	type SessionActivitySummary,
	parseActivityTree,
	isFailedDelegateOutcome,
	isFailedJobOutcome,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";

export function activityFixture(raw: unknown, params: SessionActivityReadParams) {
	const parsed = parseActivityTree(raw);
	if (!parsed) throw new Error("invalid activity test fixture");
	const tree = parsed;
	const context: SessionActivityContext = {
		ref: tree.root.ref,
		sessionId: tree.root.sessionId ?? "coord",
		rootRef: tree.root.ref,
		ancestors: [],
		ancestryKnown: true,
		epoch: "fixture",
		availability: "retained",
	};
	const delegates: SessionDelegate[] = [];
	const jobs: SessionJobsResponse["jobs"] = [];
	const issues: SessionDelegatesResponse["page"]["issues"] = [];
	let continuation: string | undefined;
	function visit(node: ActivityTree["root"], parentDelegateId?: string) {
		if (node.branch.error) issues.push({ ref: node.ref, code: "unavailable" });
		if (node.branch.truncated) continuation ??= node.branch.continuation ?? "remaining";
		for (const entry of node.entries) {
			if (entry.kind === "shell") jobs.push({ ...entry.job });
			else {
				const d = entry.delegate;
				const { child: _child, branch: _branch, message: _message, turns: _turns, mandate: _mandate, ...facts } = d;
				delegates.push({
					...facts,
					description: d.description ?? "",
					ownerRef: node.ref,
					rootRef: tree.root.ref,
					task: d.task ?? d.mandate ?? d.description ?? "",
					type: d.type ?? "delegate",
					lifecycle: d.lifecycle ?? "running",
					phase: d.phase ?? "running",
					status: d.status ?? "running",
					terminal: d.terminal ?? false,
					resumable: d.resumable ?? false,
					...(parentDelegateId ? { parentDelegateId } : {}),
				});
				if (d.child && params.scope === "subtree") visit(d.child, d.delegateId);
			}
		}
	}
	visit(tree.root);
	function count(
		rows: readonly { terminal?: boolean; outcome?: string }[],
		failed: (value: string | undefined) => boolean,
	): SessionActivityCounts {
		return {
			known: !continuation && issues.length === 0,
			total: rows.length,
			active: rows.filter((row) => !row.terminal).length,
			failed: rows.filter((row) => row.terminal && failed(row.outcome)).length,
			completed: rows.filter((row) => row.terminal && !failed(row.outcome)).length,
		};
	}
	const scope = params.scope ?? "session";
	const summary: SessionActivitySummary = {
		context,
		scope,
		delegates: count(delegates, isFailedDelegateOutcome),
		jobs: count(jobs, isFailedJobOutcome),
		watches: { known: true, total: 0, active: 0, failed: 0, completed: 0 },
	};
	return { context, scope, delegates, jobs, issues, continuation, summary };
}

/** Builds independent activity authority for a scripted thread's declared fixture resources. */
export function threadActivityFixture(thread: Thread, params: SessionActivityReadParams) {
	return activityFixture(
		{
			revision: 1,
			root: {
				label: "",
				aggregate: "working",
				counts: { active: 0, failed: 0, completed: 0, complete: true },
				branch: {},
				ref: thread.evener.ref,
				sessionId: thread.id,
				entries: (thread.evener.diagnostics?.delegates ?? []).map((delegate) => ({
					kind: "delegate",
					delegate: { ...delegate, childRef: delegate.transcriptRef, branch: {} },
				})),
			},
		},
		params,
	);
}

/** The fixture callback supplies each journal membership page, never a legacy RPC. */
export function installActivityFixture(
	client: FakeClient,
	read: (cursor?: string) => unknown | Promise<unknown>,
): void {
	const pending = new Map<string | undefined, Promise<unknown>>();
	const load = (cursor?: string) => {
		const held = pending.get(cursor);
		if (held) return held;
		const result = read(cursor);
		if (result instanceof Promise) {
			pending.set(cursor, result);
			void result.then(
				() => pending.delete(cursor),
				() => pending.delete(cursor),
			);
		}
		return result;
	};
	client.on("evener/thread/activity/read", async (params) => activityFixture(await load(), params).summary);
	client.on("evener/thread/delegates/list", async (params) => {
		const f = activityFixture(await load(params.cursor), params);
		return {
			context: f.context,
			scope: f.scope,
			delegates: f.delegates,
			page: { complete: !f.continuation, issues: f.issues, ...(f.continuation ? { nextCursor: f.continuation } : {}) },
		};
	});
	client.on("evener/thread/jobs/list", async (params) => {
		const f = activityFixture(await load(params.cursor), params);
		return {
			context: f.context,
			scope: f.scope,
			jobs: f.jobs,
			page: { complete: !f.continuation, issues: f.issues, ...(f.continuation ? { nextCursor: f.continuation } : {}) },
		};
	});
	client.on("evener/thread/watches/list", async (params) => ({
		context: activityFixture(await load(), params).context,
		scope: params.scope ?? "session",
		watches: [],
		page: { complete: true, issues: [] },
	}));
	client.on("thread/unsubscribe", () => ({}));
}
