// Typed external activity replies for native tests that share the rich row fixtures.
import {
	type ActivityDelegate,
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

// The optional SessionDelegate fields a hub's delegate rows carry, which the
// adapter copies beside the ones it always sets. A rich fixture's other
// ActivityDelegate fields (a report message, a mandate, a resolved model,
// timings, origin ids and the like) belong to the activity tree's shape, so
// the adapter never passes them on and a test can't pass on data these reads
// don't send.
const DELEGATE_FIELDS = [
	"name",
	"reportPreview",
	"reportPreviewTruncated",
	"outcome",
	"reason",
	"error",
	"notResumableReason",
	"model",
	"reasoningEffort",
	"runStartedAt",
	"runEndedAt",
	"latestActivityAt",
	"usage",
	"worktree",
] as const satisfies readonly (keyof ActivityDelegate & keyof SessionDelegate)[];

// Fails to compile when SessionDelegate gains a field the adapter neither
// sets itself nor copies through DELEGATE_FIELDS.
type UncopiedDelegateField = Exclude<
	keyof SessionDelegate,
	| (typeof DELEGATE_FIELDS)[number]
	| "delegateId"
	| "runGeneration"
	| "projectionRevision"
	| "ownerRef"
	| "rootRef"
	| "childRef"
	| "parentDelegateId"
	| "description"
	| "task"
	| "type"
	| "lifecycle"
	| "phase"
	| "status"
	| "terminal"
	| "resumable"
>;
const everyDelegateFieldCarried: [UncopiedDelegateField] extends [never] ? true : UncopiedDelegateField = true;
void everyDelegateFieldCarried;

function definedFields<T extends object, K extends keyof T>(source: T, keys: readonly K[]): Pick<T, K> {
	const picked: Partial<Pick<T, K>> = {};
	for (const key of keys) if (source[key] !== undefined) picked[key] = source[key];
	return picked as Pick<T, K>;
}

/** A fixture's context overrides: the push capability rides on the context the
 * producer builds, so a test opts in to the no-read merge by advertising it
 * here. The default omits the capability and stays on the retained read path. */
type ActivityFixtureCapability = Partial<Pick<SessionActivityContext, "availability" | "reportPreview">>;

export function activityFixture(
	raw: unknown,
	params: SessionActivityReadParams,
	capability: ActivityFixtureCapability = {},
) {
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
		...capability,
	};
	const delegates: SessionDelegate[] = [];
	const jobs: SessionJobsResponse["jobs"] = [];
	const issues: SessionDelegatesResponse["page"]["issues"] = [];
	let continuation: string | undefined;
	function visit(node: ActivityTree["root"], parentDelegateId?: string) {
		if (node.branch.error) issues.push({ ref: node.ref, code: "unavailable" });
		if (node.branch.truncated) continuation ??= node.branch.continuation ?? "remaining";
		for (const entry of node.entries) {
			if (entry.kind === "shell") {
				// A hub's job row is a JobActivityJob, which has no parentDelegateId.
				const { parentDelegateId: _parentDelegateId, ...job } = entry.job;
				jobs.push(job);
			} else {
				const d = entry.delegate;
				// The hub reports a finished delegate as idle, and closes one that
				// can't be resumed.
				const terminal = d.terminal === true;
				const state = terminal ? "idle" : "running";
				const resumable = d.resumable ?? false;
				delegates.push({
					delegateId: d.delegateId,
					runGeneration: d.runGeneration ?? 0,
					projectionRevision: d.projectionRevision ?? 0,
					ownerRef: node.ref,
					rootRef: tree.root.ref,
					childRef: d.childRef,
					...(parentDelegateId ? { parentDelegateId } : {}),
					description: d.description ?? "",
					task: d.task ?? d.description ?? "",
					type: d.type ?? "delegate",
					lifecycle: d.lifecycle ?? state,
					phase: d.phase ?? (terminal && !resumable ? "closed" : state),
					status: d.status ?? state,
					terminal,
					resumable,
					...definedFields(d, DELEGATE_FIELDS),
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
	capability: ActivityFixtureCapability = {},
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
	client.on("evener/thread/activity/read", async (params) => activityFixture(await load(), params, capability).summary);
	client.on("evener/thread/delegates/list", async (params) => {
		const f = activityFixture(await load(params.cursor), params, capability);
		return {
			context: f.context,
			scope: f.scope,
			delegates: f.delegates,
			page: { complete: !f.continuation, issues: f.issues, ...(f.continuation ? { nextCursor: f.continuation } : {}) },
		};
	});
	client.on("evener/thread/jobs/list", async (params) => {
		const f = activityFixture(await load(params.cursor), params, capability);
		return {
			context: f.context,
			scope: f.scope,
			jobs: f.jobs,
			page: { complete: !f.continuation, issues: f.issues, ...(f.continuation ? { nextCursor: f.continuation } : {}) },
		};
	});
	client.on("evener/thread/watches/list", async (params) => ({
		context: activityFixture(await load(), params, capability).context,
		scope: params.scope ?? "session",
		watches: [],
		page: { complete: true, issues: [] },
	}));
	client.on("thread/unsubscribe", () => ({}));
}
