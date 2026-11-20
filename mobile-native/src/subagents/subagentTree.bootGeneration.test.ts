// Boot generation on the phone: the coordinator's follow read names the daemon
// that serves it, and the retained subtree activity store must adopt that
// identity. Otherwise a rebooted coordinator leaves the replaced daemon's held
// delegate row showing a wait nothing can release.
import { expect, test } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { SubagentTree } from "./subagentTree";
import { installActivityFixture } from "./sessionActivityTestUtils";

function hub(): FakeClient {
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
	// The hub joins the drain hold from the delegate's own session, so it rides
	// the delegate read rather than the retained activity tree.
	client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
		context: {
			ref,
			sessionId: "coord",
			rootRef: ref,
			ancestors: [],
			ancestryKnown: true,
			epoch: "fixture",
			availability: "retained",
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
				holdSequence: 7,
			},
		],
		page: { complete: true, issues: [] },
	}));
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

async function opened(): Promise<SubagentTree> {
	const subject = new SubagentTree("local:coord", "coord");
	subject.observeActivity();
	const client = hub();
	answerReads(client, "1");
	await subject.setClient(client);
	await subject.follow();
	return subject;
}

test("a follow read adopting a replacement generation retires the replaced daemon's hold", async () => {
	const subject = await opened();
	expect(heldSequence(subject)).toBe(7);

	// The coordinator's daemon reboots as generation 2; the same store sees it.
	const client = hub();
	answerReads(client, "2");
	await subject.setClient(client);
	await subject.follow();

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
