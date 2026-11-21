// Boot generation on the phone: the coordinator's follow read names the daemon
// that serves it, and the retained subtree activity store must adopt that
// identity. Otherwise a rebooted coordinator leaves the replaced daemon's held
// delegate row showing a wait nothing can release.
import { DAEMONLESS_BOOT_GENERATION } from "@evener/appwire-client";
import { expect, test, vi } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { nextMacrotask } from "@evener/appwire-client/testing/macrotask";
import { SubagentTree } from "./subagentTree";
import { installActivityFixture } from "./sessionActivityTestUtils";

/** A coordinator hub whose delegate page reports `holdSequence: 7` while
 * `held`, the replacement daemon's own live hold of 9 while `replacement`, and
 * an unheld row otherwise. `gate` holds the delegates response until the caller
 * releases it, so a test can observe what a follow read adopts before any fresh
 * page lands. */
interface HubOptions {
	held?: boolean;
	replacement?: boolean;
	gate?: Promise<void>;
}

/** Scripts the delegates page. Re-calling it re-scripts the same client, which
 * is how a test hands one connection a replacement daemon. */
function serveDelegates(client: FakeClient, { held = true, replacement = false, gate }: HubOptions = {}): void {
	// The hub joins the drain hold from the delegate's own session, so it rides
	// the delegate read rather than the retained activity tree.
	client.on("evener/thread/delegates/list", async ({ ref, scope }) => {
		await gate;
		return {
			context: {
				ref,
				sessionId: "coord",
				rootRef: ref,
				ancestors: [],
				ancestryKnown: true,
				epoch: replacement ? "fixture-2" : "fixture",
				// A replacement daemon serving the thread is live, and advertises the
				// bounded preview, so its frames are the no-read merge rather than an
				// invalidation that re-reads the page.
				availability: replacement ? "live" : "retained",
				...(replacement ? { reportPreview: true } : {}),
			},
			scope: scope ?? "subtree",
			delegates: [
				{
					delegateId: "a",
					runGeneration: 1,
					projectionRevision: 1,
					ownerRef: ref,
					rootRef: ref,
					childRef: "local:a",
					description: "a",
					task: "a",
					type: "delegate",
					lifecycle: "running",
					phase: "running",
					status: "running",
					terminal: false,
					resumable: true,
					...(replacement
						? { drainHeldByJobs: true, holdSequence: 9, runningJobCount: 1 }
						: held
							? { holdSequence: 7 }
							: {}),
				},
			],
			page: { complete: true, issues: [] },
		};
	});
}

function hub(options: HubOptions = {}): FakeClient {
	const client = new FakeClient("ready");
	installActivityFixture(client, () => ({
		revision: 1,
		root: {
			kind: "session",
			sessionId: "coord",
			ref: "local:coord",
			label: "Coordinator",
			aggregate: "working",
			counts: { active: 1, failed: 0, completed: 0, complete: true },
			branch: {},
			entries: [],
		},
	}));
	serveDelegates(client, options);
	return client;
}

/** The follow read's answer, naming `bootGeneration` unless it is omitted. */
function answerReads(client: FakeClient, bootGeneration: string | undefined): void {
	client.on("thread/read", ({ ref }) => ({
		thread: wireThread(ref, { id: "coord", sessionId: "coord", modelProvider: "scripted/model" }),
		...(bootGeneration === undefined ? {} : { bootGeneration }),
	}));
}

const heldSequence = (subject: SubagentTree) => subject.getSnapshot().pages?.delegates.rows[0]?.holdSequence;

/** The delegate row the cached presentation renders while no store is
 * connected -- what the Agents list shows during an offline period. */
function cachedDelegate(subject: SubagentTree) {
	const entries = subject.getSnapshot().tree?.root.entries ?? [];
	for (const entry of entries) if (entry.kind === "delegate") return entry.delegate;
	return undefined;
}

async function opened(): Promise<SubagentTree> {
	const subject = new SubagentTree("local:coord", "coord");
	subject.observeActivity();
	const client = hub();
	answerReads(client, "1");
	await subject.setClient(client);
	await subject.follow();
	return subject;
}

test("a follow read adopting a replacement generation retires the retained hold", async () => {
	const subject = await opened();
	expect(heldSequence(subject)).toBe(7);

	// The coordinator's daemon reboots as generation 2. The rows the new store
	// retained have not been served by any page of its own yet, so the follow
	// read is what tells it whose samples those rows wear -- and the replacement
	// then retires them. The replacement's own page is held back until after the
	// read, so the retirement is the adoption's and not the page's.
	const page = deferred<void>();
	const client = hub({ held: false, gate: page.promise });
	answerReads(client, "2");
	void subject.setClient(client);
	await subject.follow();

	// The retained row's sample is retired at once: nothing on this client proves
	// the wait the replaced daemon numbered still holds.
	expect(heldSequence(subject)).toBeUndefined();

	// The replacement's own page reports no wait either, so nothing restores one.
	page.resolve();
	await subject.reload();
	expect(heldSequence(subject)).toBeUndefined();
});

test("a follow read naming no generation keeps the hold the hub already published", async () => {
	const subject = await opened();
	expect(heldSequence(subject)).toBe(7);

	// An older peer omits the field: nothing is said about identity, so the
	// row keeps the hold its own read published.
	const client = hub();
	answerReads(client, undefined);
	await subject.setClient(client);
	await subject.follow();

	expect(heldSequence(subject)).toBe(7);
});

test("a relay resync's replacement generation outlives the client swap that follows it", async () => {
	const subject = new SubagentTree("local:coord", "coord");
	subject.observeActivity();
	const focused = hub();
	answerReads(focused, "1");
	await subject.setClient(focused);
	await subject.follow();
	expect(heldSequence(subject)).toBe(7);

	// Relay recovery hands this same connection a replacement daemon: the
	// resync's lean thread read names generation 2 while the replacement's
	// refreshed page -- carrying the live hold that daemon sampled as 9 -- is
	// still in flight. Activity therefore learns the replacement generation
	// itself, without the follow read that used to be the only source of it.
	const landing = deferred<void>();
	serveDelegates(focused, { replacement: true, gate: landing.promise });
	answerReads(focused, "2");
	focused.emitNotification({ method: "evener/thread/resync", params: { ref: "local:coord", threadId: "coord" } });
	landing.resolve();
	await vi.waitFor(() => expect(heldSequence(subject)).toBe(9));
	expect(heldSequence(subject)).toBe(9);

	// The screen lets go before the next connection arrives.
	await subject.setClient(null);

	// The client is swapped. The new store is handed the retained rows, whose
	// samples generation 2 published -- not generation 1, which is the last
	// generation the follow read named. Its own page is held back, so the follow
	// read naming generation 2 is what tells it whose rows these are.
	const page = deferred<void>();
	const client = hub({ held: false, gate: page.promise });
	answerReads(client, "2");
	void subject.setClient(client);
	await subject.follow();

	// The retained wait is 2's own sample: naming 2 again retires nothing, so
	// the hold survives. Had the swap walked the rows back to 1, adopting 2
	// would have retired exactly this still-live wait.
	expect(heldSequence(subject)).toBe(9);
});

test("a page-served hold the replacement generation did not publish is not retained as its own", async () => {
	const subject = new SubagentTree("local:coord", "coord");
	subject.observeActivity();
	const focused = hub();
	answerReads(focused, "1");
	await subject.setClient(focused);
	await subject.follow();
	expect(heldSequence(subject)).toBe(7);

	// Relay recovery names generation 2 and the store adopts it from the recovery
	// read, while the refreshed delegates page is still in flight. The loaded
	// row's hold was served by a page, which names no generation: after the
	// transition nothing proves whether the replaced daemon or its replacement
	// sampled it.
	const landing = deferred<void>();
	serveDelegates(focused, { replacement: true, gate: landing.promise });
	answerReads(focused, "2");
	focused.emitNotification({ method: "evener/thread/resync", params: { ref: "local:coord", threadId: "coord" } });
	await nextMacrotask();

	// The screen lets go before the replacement's page lands.
	await subject.setClient(null);

	// The disconnected binding renders the tree it cached, not the store's
	// pages. The sanitized snapshot must reach that tree before the
	// disconnect publishes, so the row keeps its durable fields and loses the
	// wait nothing can attribute -- otherwise the Agents list reports it as
	// waiting on subagents for the whole offline period.
	const offline = cachedDelegate(subject);
	expect(offline?.delegateId).toBe("a");
	expect(offline?.lifecycle).toBe("running");
	expect(offline?.holdSequence).toBeUndefined();

	// The new store inherits the rows. Its page is held back, and the follow read
	// names generation 2; the unresolved sample must not be read as 2's own.
	const page = deferred<void>();
	const client = hub({ held: false, gate: page.promise });
	answerReads(client, "2");
	void subject.setClient(client);
	await subject.follow();

	expect(heldSequence(subject)).toBeUndefined();
});

test("a follow read answered from saved history keeps the live hold", async () => {
	const subject = await opened();
	expect(heldSequence(subject)).toBe(7);

	// The relay drops and the follow read answers from saved history. The hub
	// stamps `daemonless` because it fell back, not because it proved the
	// coordinator's daemon exited (docs/product/history-content.md), so the
	// still-running daemon keeps its hold.
	const client = hub();
	answerReads(client, DAEMONLESS_BOOT_GENERATION);
	await subject.setClient(client);
	await subject.follow();

	expect(heldSequence(subject)).toBe(7);
});

test("a follow read adopting a replacement generation keeps the hold its fresh page published", async () => {
	const subject = await opened();
	expect(heldSequence(subject)).toBe(7);

	// Relay recovery switches to the replacement daemon. Its refreshed page --
	// carrying the live hold the daemon now serving the coordinator sampled as 9
	// -- lands before the follow read adopts that daemon, and the replacement's
	// frames keep arriving.
	const client = hub({ replacement: true });
	answerReads(client, "2");
	await subject.setClient(client);
	await vi.waitFor(() => expect(heldSequence(subject)).toBe(9));
	// The page has landed: the row wears the replacement's own sample, not the
	// retained daemon's 7.
	expect(heldSequence(subject)).toBe(9);

	await subject.follow();

	// The follow read adopts the replacement cleanly. Its hold survives, and the
	// next frame the replacement publishes -- the same sample at an unchanged
	// revision, then the release -- is applied rather than judged retired. An
	// unchanged wait publishes nothing to restore a hold that was cleared.
	expect(heldSequence(subject)).toBe(9);
	client.emitNotification({
		method: "evener/delegate/updated",
		params: {
			ref: "local:coord",
			threadId: "coord",
			bootGeneration: "2",
			delegate: {
				runGeneration: 1,
				delegateId: "a",
				ownerSessionId: "coord",
				logicalOwnerSessionId: "coord",
				rootSessionId: "coord",
				childSessionId: "a",
				childRef: "local:a",
				transcriptRef: "local:a",
				type: "delegate",
				lifecycle: "running",
				phase: "running",
				status: "running",
				terminal: false,
				resumable: true,
				needsAttention: false,
				projectionRevision: 1,
				holdSequence: 10,
			},
		},
	} as Parameters<typeof client.emitNotification>[0]);
	expect(heldSequence(subject)).toBe(10);
});
