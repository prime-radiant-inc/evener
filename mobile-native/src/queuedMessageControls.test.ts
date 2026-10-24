import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, test, vi } from "vitest";
import { buildComposerInput, type InputAttachment } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { DraftDocument } from "./draftDocument";
import { DraftLibrary } from "./draftLibrary";
import { DraftRepository } from "./draftRepository";
import { NativeMutationRuntime, nativeMutationTargetKey, type NativeMutationRequest } from "./nativeMutationRuntime";
import {
	editQueuedMessageIntoDraft,
	reconcileEditedResends,
	removeQueuedMessageWithDraftCapture,
} from "./queuedMessageControls";
import { openSqliteSyncDouble } from "./sqliteSync.testkit";

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", async () => ({
	randomUUID: (await import("node:crypto")).randomUUID,
	getRandomValues: (array: Uint8Array) => array,
}));
const cleanups: Array<() => Promise<void>> = [];
afterEach(async () => {
	for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});
const destination = { hubId: "hub", sessionRef: "session" };
const key = nativeMutationTargetKey(destination.hubId, destination.sessionRef);
function request(extra: Partial<NativeMutationRequest> = {}): NativeMutationRequest {
	return {
		hubId: "hub",
		targetRef: "session",
		threadId: "thread",
		instanceId: "instance",
		kind: "send",
		input: [{ type: "text", text: "same" }],
		...extra,
	} as NativeMutationRequest;
}
function fixture() {
	const directory = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "queued-controls-"));
	const draftPath = join(directory, "drafts.sqlite");
	const mutationPath = join(directory, "mutations.sqlite");
	let drafts = openSqliteSyncDouble(draftPath);
	let mutations = openSqliteSyncDouble(mutationPath);
	let next = 0;
	const options = { createMutationId: () => `mutation-${++next}`, setInterval: () => 1, clearInterval: () => {} };
	let runtime = new NativeMutationRuntime(mutations.port, options);
	let repository = new DraftRepository(drafts.port);
	let document = new DraftDocument(() => repository, destination);
	let client = new FakeClient("closed");
	const clients = [client];
	const register = () =>
		runtime.registerTarget("hub", "session", client, { threadId: "thread", instanceId: "instance" });
	let unregisterTarget = register();
	const f = {
		get runtime() {
			return runtime;
		},
		get document() {
			return document;
		},
		get repository() {
			return repository;
		},
		get drafts() {
			return drafts.database;
		},
		get mutations() {
			return mutations.database;
		},
		get client() {
			return client;
		},
		get calls() {
			return clients.flatMap((transport) => transport.calls);
		},
		connect() {
			client = new FakeClient("ready");
			clients.push(client);
			unregisterTarget = register();
		},
		unregisterTarget() {
			unregisterTarget();
		},
		async reopen() {
			await runtime.stop();
			document.forget();
			drafts.database.close();
			mutations.database.close();
			drafts = openSqliteSyncDouble(draftPath);
			mutations = openSqliteSyncDouble(mutationPath);
			runtime = new NativeMutationRuntime(mutations.port, options);
			repository = new DraftRepository(drafts.port);
			document = new DraftDocument(() => repository, destination);
			unregisterTarget = register();
		},
		reloadDraft() {
			document.forget();
			document = new DraftDocument(() => repository, destination);
		},
		async enqueue(extra: Partial<NativeMutationRequest> = {}) {
			await runtime.submit(request(extra));
			const rows = (await runtime.read(key)).outbox;
			return rows[rows.length - 1].clientMutationId;
		},
	};
	cleanups.push(async () => {
		await runtime.stop();
		drafts.database.close();
		mutations.database.close();
		rmSync(directory, { recursive: true });
	});
	return f;
}

test("offline edit and remove distinguish identical messages and restore exact IDs after reopen", async () => {
	const f = fixture();
	const a = await f.enqueue();
	const b = await f.enqueue();
	f.document.edit("current");
	expect(await editQueuedMessageIntoDraft(f.runtime, key, a, f.document)).toBe("restored");
	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, b, f.document)).toBe(true);
	expect(f.document.getSnapshot().record.draft).toBe("current\n\nsame");
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([a]);
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	await f.reopen();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, a, f.document)).toBe("already-restored");
	expect(await editQueuedMessageIntoDraft(f.runtime, key, b, f.document)).toBe("lost");
	expect(f.document.getSnapshot().record.draft).toBe("current\n\nsame");
	expect(f.client.calls).toEqual([]);
});

test("interrupted Edit recovery restores exact composition after retarget to a different ref and instance", async () => {
	const f = fixture();
	const captured = await richSource(f);
	await f.enqueue();
	f.document.edit("current");
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("restored");

	const resumedRef = "session-2";
	const resumedKey = nativeMutationTargetKey(destination.hubId, resumedRef);
	expect(
		f.runtime.storage.retarget(key, {
			ref: resumedKey,
			wireRef: resumedRef,
			threadId: "thread-2",
			instanceId: "instance-2",
		}),
	).toHaveLength(1);
	await f.reopen();
	f.runtime.registerTarget(destination.hubId, resumedRef, f.client, {
		threadId: "thread-2",
		instanceId: "instance-2",
	});
	const [recovered] = (await f.runtime.read(resumedKey)).recovery;
	expect(recovered).toMatchObject({
		clientMutationId: captured,
		targetRef: resumedKey,
		threadId: "thread-2",
		instanceId: "instance-2",
		recoveryKind: "editing",
		composerText: "[image 9] /skill",
	});

	const resumed = new DraftDocument(() => f.repository, { hubId: destination.hubId, sessionRef: resumedRef });
	expect(await editQueuedMessageIntoDraft(f.runtime, resumedKey, captured, resumed)).toBe("restored");
	expect(resumed.getSnapshot().record).toMatchObject({
		draft: "[image 1] /skill",
		images: [expect.objectContaining({ marker: 1 })],
		composer: {
			skillNames: ["skill"],
			commandNames: ["command"],
			mentions: [{ kind: "skill", name: "skill", offset: 10 }],
			recoveryIds: [captured],
		},
	});
});

test("a resume carries the edited recovered composition to the new ref and retires the old draft", async () => {
	const f = fixture();
	const captured = await f.enqueue();
	f.document.edit("current");
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("restored");
	f.document.edit("edited recovered text");
	// Another waiting message is what drives the resume.
	await f.enqueue();

	const resumedRef = "session-resumed";
	const resumedKey = nativeMutationTargetKey(destination.hubId, resumedRef);
	const resumedDestination = { hubId: destination.hubId, sessionRef: resumedRef };
	expect(
		f.runtime.storage.retarget(key, {
			ref: resumedKey,
			wireRef: resumedRef,
			threadId: "thread-2",
			instanceId: "instance-2",
		}),
	).toHaveLength(1);

	const library = new DraftLibrary(() => f.repository);
	expect(library.handoff(destination, resumedDestination)).toBe(true);
	const resumed = library.open(resumedDestination);
	expect(resumed.getSnapshot().record).toMatchObject({
		draft: "edited recovered text",
		composer: { recoveryIds: [captured] },
	});
	expect((await f.runtime.read(resumedKey)).recovery).toMatchObject([
		{ clientMutationId: captured, targetRef: resumedKey },
	]);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect(f.repository.read(destination)).toEqual({ draft: "", unconfirmed: null });
});

test("a migrated resend receipt reconciles under the resumed ref and clears its capture", async () => {
	const f = fixture();
	const captured = await f.enqueue();
	f.document.edit("current");
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("restored");
	f.document.edit("edited recovered text");
	// The Send already admitted its resend receipt under the old ref.
	await explicitSend(f);

	const resumedRef = "session-resumed";
	const resumedKey = nativeMutationTargetKey(destination.hubId, resumedRef);
	const resumedDestination = { hubId: destination.hubId, sessionRef: resumedRef };
	expect(
		f.runtime.storage.retarget(key, {
			ref: resumedKey,
			wireRef: resumedRef,
			threadId: "thread-2",
			instanceId: "instance-2",
		}),
	).toHaveLength(1);

	const library = new DraftLibrary(() => f.repository);
	expect(library.handoff(destination, resumedDestination)).toBe(true);
	const resumed = library.open(resumedDestination);
	expect(await f.runtime.readRecoveryResend(resumedKey, [captured])).toMatchObject({
		targetRef: resumedKey,
		sourceIds: [captured],
	});

	await reconcileEditedResends(f.runtime, resumedKey, resumed);
	expect(resumed.getSnapshot().record).toMatchObject({ unconfirmed: null });
	expect(await f.runtime.readRecoveryResend(resumedKey, [captured])).toBeUndefined();
	expect(f.repository.read(resumedDestination)).toEqual({ draft: "", unconfirmed: null });
});

test("successful built-in completion durably cleans only captured sources without replay", async () => {
	const f = fixture();
	const captured = await f.enqueue();
	const sibling = await f.enqueue();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("restored");
	let commands = 0;
	await f.document.submitBuiltinCommand(async () => {
		commands++;
		return true;
	});
	expect(f.repository.read(destination)).toMatchObject({
		draft: "",
		unconfirmed: null,
		completedCommandRecoveryIds: [captured],
	});
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("already-restored");
	await f.reopen();
	await reconcileEditedResends(f.runtime, key, f.document);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect((await f.runtime.read(key)).outbox.map((source) => source.clientMutationId)).toEqual([sibling]);
	expect(f.document.getSnapshot().record.completedCommandRecoveryIds).toBeUndefined();
	expect(commands).toBe(1);
	expect(f.client.calls).toEqual([]);
});

test("an occupied resumed destination discards a completed built-in command instead of re-appending it", async () => {
	const f = fixture();
	const captured = await f.enqueue();
	f.document.edit("current");
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("restored");
	await f.document.submitBuiltinCommand(async () => true);
	expect(f.repository.read(destination).completedCommandRecoveryIds).toEqual([captured]);
	// retarget moves recovery rows alongside the waiting outbox rows a resume
	// carries; this message is only the resume's reason to run.
	await f.enqueue();

	const resumedRef = "session-resumed";
	const resumedKey = nativeMutationTargetKey(destination.hubId, resumedRef);
	const resumedDestination = { hubId: destination.hubId, sessionRef: resumedRef };
	expect(
		f.runtime.storage.retarget(key, {
			ref: resumedKey,
			wireRef: resumedRef,
			threadId: "thread-2",
			instanceId: "instance-2",
		}),
	).toHaveLength(1);
	f.runtime.registerTarget(destination.hubId, resumedRef, f.client, {
		threadId: "thread-2",
		instanceId: "instance-2",
	});

	// The resumed destination already holds work, so it refuses the handoff.
	f.repository.write(resumedDestination, { draft: "occupied destination", unconfirmed: null });
	const library = new DraftLibrary(() => f.repository);
	const resumed = library.open(resumedDestination);
	expect(library.handoff(destination, resumedDestination)).toBe(false);
	expect(resumed.getSnapshot().record.draft).toBe("occupied destination");

	// The screen's recovery step discards the completed row before restoring
	// anything, so a later Send cannot append the command again.
	await reconcileEditedResends(f.runtime, resumedKey, resumed);
	for (const row of (await f.runtime.read(resumedKey)).recovery) {
		if (row.recoveryKind === "editing")
			await editQueuedMessageIntoDraft(f.runtime, resumedKey, row.clientMutationId, resumed);
	}
	expect(resumed.getSnapshot().record.draft).toBe("occupied destination");
	expect((await f.runtime.read(resumedKey)).recovery).toEqual([]);
	expect(resumed.getSnapshot().record.completedCommandRecoveryIds).toBeUndefined();
});

test("a draft-database write failure leaves a durable handoff the restart retries", async () => {
	const f = fixture();
	const captured = await f.enqueue();
	f.document.edit("current");
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("restored");
	f.document.edit("edited recovered");
	// A resume needs a waiting message; the retarget below moves it with the
	// recovery row and records the identity edge in the mutations database.
	await f.enqueue();

	const resumedRef = "session-resumed";
	const resumedKey = nativeMutationTargetKey(destination.hubId, resumedRef);
	const resumedDestination = { hubId: destination.hubId, sessionRef: resumedRef };
	expect(
		f.runtime.storage.retarget(key, {
			ref: resumedKey,
			wireRef: resumedRef,
			threadId: "thread-2",
			instanceId: "instance-2",
		}),
	).toHaveLength(1);

	// Every write to the drafts database fails, including the pending mapping
	// the transfer would otherwise record there.
	f.drafts.exec("PRAGMA query_only = ON");
	const beforeRestart = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	expect(beforeRestart.handoff(destination, resumedDestination)).toBe(false);
	expect(f.repository.read(destination).draft).toBe("edited recovered");

	// A restart: the drafts database is writable again but never held the
	// mapping. The mutations database's resume edge still names the handoff.
	await f.reopen();
	const restarted = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	expect(restarted.hasPendingHandoff(resumedDestination)).toBe(true);
	expect(restarted.open(resumedDestination).getSnapshot().record.draft).toBe("");
	restarted.retryPendingHandoffs();
	expect(restarted.hasPendingHandoff(resumedDestination)).toBe(false);
	expect(f.runtime.storage.readResumeIdentities()).toEqual([]);
	expect(f.repository.read(resumedDestination)).toMatchObject({
		draft: "edited recovered",
		composer: { recoveryIds: [captured] },
	});
	expect(f.repository.read(destination)).toEqual({ draft: "", unconfirmed: null });
});

test("a chained resume redirects a pending predecessor and keeps the destination pending", async () => {
	const f = fixture();
	// A completed built-in marker and an edited composition both live at A.
	const completed = await f.enqueue();
	f.document.edit("current");
	await editQueuedMessageIntoDraft(f.runtime, key, completed, f.document);
	await f.document.submitBuiltinCommand(async () => true);
	const captured = await f.enqueue();
	await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document);
	f.document.edit("edited recovered");
	// A waiting message moves with the recovery rows on each retarget.
	await f.enqueue();
	expect(f.repository.read(destination)).toMatchObject({
		draft: "edited recovered",
		composer: { recoveryIds: [captured] },
		completedCommandRecoveryIds: [completed],
	});

	const bRef = "session-b";
	const cRef = "session-c";
	const bDestination = { hubId: destination.hubId, sessionRef: bRef };
	const cDestination = { hubId: destination.hubId, sessionRef: cRef };
	const bKey = nativeMutationTargetKey(destination.hubId, bRef);
	const cKey = nativeMutationTargetKey(destination.hubId, cRef);
	expect(
		f.runtime.storage.retarget(key, {
			ref: bKey,
			wireRef: bRef,
			threadId: "thread-b",
			instanceId: "instance-b",
		}),
	).toHaveLength(1);
	const library = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);

	// A->B is pending because every drafts write is refused.
	f.drafts.exec("PRAGMA query_only = ON");
	expect(library.handoff(destination, bDestination)).toBe(false);
	f.drafts.exec("PRAGMA query_only = OFF");

	// B resolves to C before A->B retries. B holds no composition of its own.
	expect(
		f.runtime.storage.retarget(bKey, {
			ref: cKey,
			wireRef: cRef,
			threadId: "thread-c",
			instanceId: "instance-c",
		}),
	).toHaveLength(1);
	expect(library.handoff(bDestination, cDestination)).toBe(true);
	expect(library.hasPendingHandoff(cDestination)).toBe(true);

	library.retryPendingHandoffs();
	expect(f.repository.read(cDestination)).toMatchObject({
		draft: "edited recovered",
		composer: { recoveryIds: [captured] },
		completedCommandRecoveryIds: [completed],
	});
	expect(f.repository.read(bDestination)).toEqual({ draft: "", unconfirmed: null });
	expect(f.repository.read(destination)).toEqual({ draft: "", unconfirmed: null });
	expect(library.hasPendingHandoff(cDestination)).toBe(false);
	expect(f.runtime.storage.readResumeIdentities()).toEqual([]);
});

test("a redirected handoff replaces the persisted predecessor before restart", async () => {
	const f = fixture();
	const completed = await f.enqueue();
	f.document.edit("current");
	await editQueuedMessageIntoDraft(f.runtime, key, completed, f.document);
	await f.document.submitBuiltinCommand(async () => true);
	const captured = await f.enqueue();
	await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document);
	f.document.edit("edited recovered");
	await f.enqueue();

	const bRef = "session-b";
	const cRef = "session-c";
	const bDestination = { hubId: destination.hubId, sessionRef: bRef };
	const cDestination = { hubId: destination.hubId, sessionRef: cRef };
	const bKey = nativeMutationTargetKey(destination.hubId, bRef);
	const cKey = nativeMutationTargetKey(destination.hubId, cRef);
	expect(
		f.runtime.storage.retarget(key, {
			ref: bKey,
			wireRef: bRef,
			threadId: "thread-b",
			instanceId: "instance-b",
		}),
	).toHaveLength(1);
	const library = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);

	// The transfer to B fails while the drafts database still accepts writes,
	// so the A->B mapping is persisted in pending_draft_handoffs.
	f.drafts.exec(
		"CREATE TRIGGER reject_b_draft BEFORE INSERT ON drafts WHEN NEW.session_ref = 'session-b' BEGIN SELECT RAISE(ABORT, 'write rejected'); END",
	);
	expect(library.handoff(destination, bDestination)).toBe(false);
	expect(f.repository.read(destination).draft).toBe("edited recovered");
	f.drafts.exec("DROP TRIGGER reject_b_draft");

	// B resolves to C before A->B retries; the predecessor must be replaced,
	// not merely joined by a second mapping.
	expect(
		f.runtime.storage.retarget(bKey, {
			ref: cKey,
			wireRef: cRef,
			threadId: "thread-c",
			instanceId: "instance-c",
		}),
	).toHaveLength(1);
	expect(library.handoff(bDestination, cDestination)).toBe(true);
	expect(library.hasPendingHandoff(cDestination)).toBe(true);

	// Restart before any retry: the retired B mapping must not come back.
	await f.reopen();
	const restarted = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	expect(restarted.hasPendingHandoff(cDestination)).toBe(true);
	expect(restarted.hasPendingHandoff(bDestination)).toBe(false);
	restarted.retryPendingHandoffs();
	expect(f.repository.read(cDestination)).toMatchObject({
		draft: "edited recovered",
		composer: { recoveryIds: [captured] },
		completedCommandRecoveryIds: [completed],
	});
	expect(f.repository.read(bDestination)).toEqual({ draft: "", unconfirmed: null });
	expect(f.repository.read(destination)).toEqual({ draft: "", unconfirmed: null });
	expect(restarted.hasPendingHandoff(cDestination)).toBe(false);
	expect(f.runtime.storage.readResumeIdentities()).toEqual([]);
});

test("a redirect rejected by one store still reaches the final destination after restart", async () => {
	const f = fixture();
	const completed = await f.enqueue();
	f.document.edit("current");
	await editQueuedMessageIntoDraft(f.runtime, key, completed, f.document);
	await f.document.submitBuiltinCommand(async () => true);
	const captured = await f.enqueue();
	await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document);
	f.document.edit("edited recovered");
	await f.enqueue();

	const bRef = "session-b";
	const cRef = "session-c";
	const bDestination = { hubId: destination.hubId, sessionRef: bRef };
	const cDestination = { hubId: destination.hubId, sessionRef: cRef };
	const bKey = nativeMutationTargetKey(destination.hubId, bRef);
	const cKey = nativeMutationTargetKey(destination.hubId, cRef);
	expect(
		f.runtime.storage.retarget(key, {
			ref: bKey,
			wireRef: bRef,
			threadId: "thread-b",
			instanceId: "instance-b",
		}),
	).toHaveLength(1);
	const library = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);

	// Persist A->B by failing only B's draft INSERT.
	f.drafts.exec(
		"CREATE TRIGGER reject_b_draft BEFORE INSERT ON drafts WHEN NEW.session_ref = 'session-b' BEGIN SELECT RAISE(ABORT, 'write rejected'); END",
	);
	expect(library.handoff(destination, bDestination)).toBe(false);
	f.drafts.exec("DROP TRIGGER reject_b_draft");

	// B->C resolves, but the drafts database rejects the redirect while the
	// mutations database accepts it. B->C must stay pending.
	expect(
		f.runtime.storage.retarget(bKey, {
			ref: cKey,
			wireRef: cRef,
			threadId: "thread-c",
			instanceId: "instance-c",
		}),
	).toHaveLength(1);
	f.drafts.exec(
		"CREATE TRIGGER reject_pending_c BEFORE INSERT ON pending_draft_handoffs WHEN NEW.to_session_ref = 'session-c' BEGIN SELECT RAISE(ABORT, 'redirect rejected'); END",
	);
	const resolved = library.handoff(bDestination, cDestination);
	f.drafts.exec("DROP TRIGGER reject_pending_c");

	// Restart: the loaded graph must canonicalize A->B through the retained
	// B->C edge before any transfer, so nothing retries into retired B.
	await f.reopen();
	const restarted = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	expect(restarted.hasPendingHandoff(cDestination)).toBe(true);
	expect(restarted.hasPendingHandoff(bDestination)).toBe(false);
	restarted.retryPendingHandoffs();
	expect(f.repository.read(cDestination)).toMatchObject({
		draft: "edited recovered",
		composer: { recoveryIds: [captured] },
		completedCommandRecoveryIds: [completed],
	});
	expect(f.repository.read(bDestination)).toEqual({ draft: "", unconfirmed: null });
	expect(f.repository.read(destination)).toEqual({ draft: "", unconfirmed: null });
	expect(restarted.hasPendingHandoff(cDestination)).toBe(false);
	expect(f.runtime.storage.readResumeIdentities()).toEqual([]);
	expect(resolved).toBe(false);
});

test("restart settles B before A so B's composition and markers win at C", async () => {
	const f = fixture();
	const completedA = await f.enqueue();
	f.document.edit("current");
	await editQueuedMessageIntoDraft(f.runtime, key, completedA, f.document);
	await f.document.submitBuiltinCommand(async () => true);
	const capturedA = await f.enqueue();
	await editQueuedMessageIntoDraft(f.runtime, key, capturedA, f.document);
	f.document.edit("A draft");
	await f.enqueue();

	const bRef = "session-b";
	const cRef = "session-c";
	const bDestination = { hubId: destination.hubId, sessionRef: bRef };
	const cDestination = { hubId: destination.hubId, sessionRef: cRef };
	const bKey = nativeMutationTargetKey(destination.hubId, bRef);
	const cKey = nativeMutationTargetKey(destination.hubId, cRef);
	expect(
		f.runtime.storage.retarget(key, {
			ref: bKey,
			wireRef: bRef,
			threadId: "thread-b",
			instanceId: "instance-b",
		}),
	).toHaveLength(1);
	const library = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	f.drafts.exec(
		"CREATE TRIGGER reject_b_draft BEFORE INSERT ON drafts WHEN NEW.session_ref = 'session-b' BEGIN SELECT RAISE(ABORT, 'write rejected'); END",
	);
	expect(library.handoff(destination, bDestination)).toBe(false);
	f.drafts.exec("DROP TRIGGER reject_b_draft");

	// B has its own composition and completion markers before the app stops.
	f.repository.write(bDestination, {
		draft: "B draft",
		unconfirmed: null,
		composer: { recoveryIds: ["captured-b"] },
		completedCommandRecoveryIds: ["completed-b"],
	});
	expect(
		f.runtime.storage.retarget(bKey, {
			ref: cKey,
			wireRef: cRef,
			threadId: "thread-c",
			instanceId: "instance-c",
		}),
	).toHaveLength(1);

	await f.reopen();
	const restarted = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	restarted.retryPendingHandoffs();
	expect(f.repository.read(cDestination)).toMatchObject({
		draft: "B draft",
		composer: { recoveryIds: ["captured-b"] },
		completedCommandRecoveryIds: expect.arrayContaining(["completed-b"]),
	});
	expect(f.repository.read(bDestination)).toEqual({ draft: "", unconfirmed: null });
	expect(f.repository.read(destination)).toMatchObject({ draft: "A draft" });
	expect(restarted.hasPendingHandoff(cDestination)).toBe(false);
});

test("a transient B transfer failure keeps A blocked until B recovers", async () => {
	const f = fixture();
	const completedA = await f.enqueue();
	f.document.edit("current");
	await editQueuedMessageIntoDraft(f.runtime, key, completedA, f.document);
	await f.document.submitBuiltinCommand(async () => true);
	const capturedA = await f.enqueue();
	await editQueuedMessageIntoDraft(f.runtime, key, capturedA, f.document);
	f.document.edit("A draft");
	await f.enqueue();

	const bRef = "session-b";
	const cRef = "session-c";
	const bDestination = { hubId: destination.hubId, sessionRef: bRef };
	const cDestination = { hubId: destination.hubId, sessionRef: cRef };
	const bKey = nativeMutationTargetKey(destination.hubId, bRef);
	const cKey = nativeMutationTargetKey(destination.hubId, cRef);
	expect(
		f.runtime.storage.retarget(key, {
			ref: bKey,
			wireRef: bRef,
			threadId: "thread-b",
			instanceId: "instance-b",
		}),
	).toHaveLength(1);
	const library = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	f.drafts.exec(
		"CREATE TRIGGER reject_b_draft BEFORE INSERT ON drafts WHEN NEW.session_ref = 'session-b' BEGIN SELECT RAISE(ABORT, 'write rejected'); END",
	);
	expect(library.handoff(destination, bDestination)).toBe(false);
	f.drafts.exec("DROP TRIGGER reject_b_draft");

	f.repository.write(bDestination, {
		draft: "B draft",
		unconfirmed: null,
		composer: { recoveryIds: ["captured-b"] },
		completedCommandRecoveryIds: ["completed-b"],
	});
	expect(
		f.runtime.storage.retarget(bKey, {
			ref: cKey,
			wireRef: cRef,
			threadId: "thread-c",
			instanceId: "instance-c",
		}),
	).toHaveLength(1);

	await f.reopen();
	const restarted = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	expect(restarted.hasPendingHandoff(cDestination)).toBe(true);

	// B's transfer fails once and B stays pending. A must not take C meanwhile.
	f.drafts.exec(
		"CREATE TRIGGER reject_b_delete BEFORE DELETE ON drafts WHEN OLD.session_ref = 'session-b' BEGIN SELECT RAISE(ABORT, 'transfer failed'); END",
	);
	restarted.retryPendingHandoffs();
	expect(f.repository.read(cDestination).draft).toBe("");
	expect(f.repository.read(destination).draft).toBe("A draft");
	expect(f.repository.read(bDestination).draft).toBe("B draft");
	expect(restarted.hasPendingHandoff(cDestination)).toBe(true);

	f.drafts.exec("DROP TRIGGER reject_b_delete");
	restarted.retryPendingHandoffs();
	expect(f.repository.read(cDestination)).toMatchObject({
		draft: "B draft",
		composer: { recoveryIds: ["captured-b"] },
		completedCommandRecoveryIds: expect.arrayContaining(["completed-b"]),
	});
	expect(f.repository.read(bDestination)).toEqual({ draft: "", unconfirmed: null });
	expect(f.repository.read(destination)).toMatchObject({ draft: "A draft" });
});

test("two restarts before settlement keep B before A", async () => {
	const f = fixture();
	const completedA = await f.enqueue();
	f.document.edit("current");
	await editQueuedMessageIntoDraft(f.runtime, key, completedA, f.document);
	await f.document.submitBuiltinCommand(async () => true);
	const capturedA = await f.enqueue();
	await editQueuedMessageIntoDraft(f.runtime, key, capturedA, f.document);
	f.document.edit("A draft");
	await f.enqueue();

	const bRef = "session-b";
	const cRef = "session-c";
	const bDestination = { hubId: destination.hubId, sessionRef: bRef };
	const cDestination = { hubId: destination.hubId, sessionRef: cRef };
	const bKey = nativeMutationTargetKey(destination.hubId, bRef);
	const cKey = nativeMutationTargetKey(destination.hubId, cRef);
	expect(
		f.runtime.storage.retarget(key, {
			ref: bKey,
			wireRef: bRef,
			threadId: "thread-b",
			instanceId: "instance-b",
		}),
	).toHaveLength(1);
	const library = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	f.drafts.exec(
		"CREATE TRIGGER reject_b_draft BEFORE INSERT ON drafts WHEN NEW.session_ref = 'session-b' BEGIN SELECT RAISE(ABORT, 'write rejected'); END",
	);
	expect(library.handoff(destination, bDestination)).toBe(false);
	f.drafts.exec("DROP TRIGGER reject_b_draft");

	f.repository.write(bDestination, {
		draft: "B draft",
		unconfirmed: null,
		composer: { recoveryIds: ["captured-b"] },
		completedCommandRecoveryIds: ["completed-b"],
	});
	expect(
		f.runtime.storage.retarget(bKey, {
			ref: cKey,
			wireRef: cRef,
			threadId: "thread-c",
			instanceId: "instance-c",
		}),
	).toHaveLength(1);

	// First restart loads and redirects A->B to A->C, but nothing settles.
	await f.reopen();
	const first = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	expect(first.hasPendingHandoff(cDestination)).toBe(true);

	// Second restart must reconstruct B->C before A->C from the durable chain.
	await f.reopen();
	const second = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	second.retryPendingHandoffs();
	expect(f.repository.read(cDestination)).toMatchObject({
		draft: "B draft",
		composer: { recoveryIds: ["captured-b"] },
		completedCommandRecoveryIds: expect.arrayContaining(["completed-b"]),
	});
	expect(f.repository.read(bDestination)).toEqual({ draft: "", unconfirmed: null });
	expect(f.repository.read(destination)).toMatchObject({ draft: "A draft" });
});

test("a loading redirect failure keeps the intermediate edge across two restarts", async () => {
	const f = fixture();
	const completedA = await f.enqueue();
	f.document.edit("current");
	await editQueuedMessageIntoDraft(f.runtime, key, completedA, f.document);
	await f.document.submitBuiltinCommand(async () => true);
	const capturedA = await f.enqueue();
	await editQueuedMessageIntoDraft(f.runtime, key, capturedA, f.document);
	f.document.edit("A draft");
	await f.enqueue();

	const bRef = "session-b";
	const cRef = "session-c";
	const bDestination = { hubId: destination.hubId, sessionRef: bRef };
	const cDestination = { hubId: destination.hubId, sessionRef: cRef };
	const bKey = nativeMutationTargetKey(destination.hubId, bRef);
	const cKey = nativeMutationTargetKey(destination.hubId, cRef);
	expect(
		f.runtime.storage.retarget(key, {
			ref: bKey,
			wireRef: bRef,
			threadId: "thread-b",
			instanceId: "instance-b",
		}),
	).toHaveLength(1);
	const library = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	f.drafts.exec(
		"CREATE TRIGGER reject_b_draft BEFORE INSERT ON drafts WHEN NEW.session_ref = 'session-b' BEGIN SELECT RAISE(ABORT, 'write rejected'); END",
	);
	expect(library.handoff(destination, bDestination)).toBe(false);
	f.drafts.exec("DROP TRIGGER reject_b_draft");

	// C already holds work, and the drafts store keeps rejecting the redirect
	// from A through B while the mutations store accepts it.
	f.repository.write(cDestination, { draft: "C occupied", unconfirmed: null });
	expect(
		f.runtime.storage.retarget(bKey, {
			ref: cKey,
			wireRef: cRef,
			threadId: "thread-c",
			instanceId: "instance-c",
		}),
	).toHaveLength(1);
	f.drafts.exec(
		"CREATE TRIGGER reject_pending_c BEFORE INSERT ON pending_draft_handoffs WHEN NEW.to_session_ref = 'session-c' BEGIN SELECT RAISE(ABORT, 'redirect rejected'); END",
	);
	expect(library.handoff(bDestination, cDestination)).toBe(false);

	// First restart: the drafts redirect still fails. The intermediate edge must
	// stay pending and A must not be allowed to settle.
	await f.reopen();
	const first = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	expect(first.hasPendingHandoff(bDestination)).toBe(true);
	expect(first.hasPendingHandoff(cDestination)).toBe(true);
	first.retryPendingHandoffs();
	expect(first.hasPendingHandoff(bDestination)).toBe(true);
	expect(first.hasPendingHandoff(cDestination)).toBe(true);
	expect(f.repository.read(destination).draft).toBe("A draft");
	expect(f.repository.read(cDestination).draft).toBe("C occupied");
	expect(f.runtime.storage.readResumeIdentities().length).toBeGreaterThan(0);

	// Second restart after the drafts store recovers: the redirect completes,
	// the intermediate refuses the occupied destination, and A stays at A.
	f.drafts.exec("DROP TRIGGER reject_pending_c");
	await f.reopen();
	const second = new DraftLibrary(
		() => f.repository,
		undefined,
		() => f.runtime.storage,
	);
	second.retryPendingHandoffs();
	expect(f.repository.read(destination).draft).toBe("A draft");
	expect(f.repository.read(bDestination)).toEqual({ draft: "", unconfirmed: null });
	expect(f.repository.read(cDestination)).toMatchObject({ draft: "C occupied" });
	expect(second.hasPendingHandoff(cDestination)).toBe(false);
	expect(f.runtime.storage.readResumeIdentities()).toEqual([]);
});

test("multiple-source completion preserves newer draft, image, and older cleanup markers", async () => {
	const f = fixture();
	const first = await richSource(f);
	const second = await richSource(f);
	await editQueuedMessageIntoDraft(f.runtime, key, first, f.document);
	await editQueuedMessageIntoDraft(f.runtime, key, second, f.document);
	f.repository.write(destination, {
		...f.document.getSnapshot().record,
		completedCommandRecoveryIds: ["older-cleanup"],
	});
	f.reloadDraft();
	let commands = 0;
	await f.document.submitBuiltinCommand(async () => {
		commands++;
		f.document.edit("newer /skill");
		f.document.addImage({ id: "typed-image", marker: 20, mediaType: "image/png", data: "AQID" });
		return true;
	});
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer /skill[image 20]",
		images: [{ id: "typed-image", marker: 20 }],
		completedCommandRecoveryIds: ["older-cleanup", first, second],
	});
	await reconcileEditedResends(f.runtime, key, f.document);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect(f.document.getSnapshot().record.completedCommandRecoveryIds).toBeUndefined();
	expect(commands).toBe(1);
});

test("ordinary successful submit retains captured recovery until matching real resend receipt", async () => {
	const f = fixture();
	const id = await richSource(f);
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
	await f.document.submit(async () => true);
	expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toEqual([id]);
	expect(f.document.getSnapshot().record.recoveryResendAcks).toBeUndefined();
	await f.runtime.submit(request({ recoverySourceIds: [id] }));
	const receipt = await f.runtime.readRecoveryResend(key, [id]);
	expect(receipt).toBeDefined();
	expect(f.document.settleEditedResend(receipt!)).toBe(true);
	expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
});

test("failed built-in command preserves captured recovery without cleanup intent", async () => {
	const f = fixture();
	const captured = await f.enqueue();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("restored");
	await f.document.submitBuiltinCommand(async () => false);
	expect(f.repository.read(destination)).toMatchObject({
		unconfirmed: "same",
		unconfirmedComposer: { recoveryIds: [captured] },
	});
	expect(f.repository.read(destination).completedCommandRecoveryIds).toBeUndefined();
	expect((await f.runtime.read(key)).recovery.map((source) => source.clientMutationId)).toEqual([captured]);
	expect(f.client.calls).toEqual([]);
});

test("failed command-success save retries durable cleanup without rerunning the command", async () => {
	const f = fixture();
	const captured = await f.enqueue();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, captured, f.document)).toBe("restored");
	f.drafts.exec(`CREATE TRIGGER reject_command_completion BEFORE UPDATE ON drafts
		WHEN NEW.completed_command_recovery_ids IS NOT NULL BEGIN SELECT RAISE(ABORT, 'completion save rejected'); END`);
	let commands = 0;
	await expect(
		f.document.submitBuiltinCommand(async () => {
			commands++;
			return true;
		}),
	).rejects.toBeInstanceOf(Error);
	expect(f.document.getSnapshot().error).not.toBeNull();
	expect((await f.runtime.read(key)).recovery.map((source) => source.clientMutationId)).toEqual([captured]);
	f.drafts.exec("DROP TRIGGER reject_command_completion");
	f.document.retry();
	expect(f.repository.read(destination).completedCommandRecoveryIds).toEqual([captured]);
	await reconcileEditedResends(f.runtime, key, f.document);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect(commands).toBe(1);
});

test.each(["first-delete", "second-delete"] as const)(
	"command cleanup retries after %s failure across reopen without touching sibling targets",
	async (cut) => {
		const f = fixture();
		const first = await f.enqueue();
		const second = await f.enqueue();
		const sibling = await f.enqueue();
		const otherTargetKey = nativeMutationTargetKey("hub", "other-session");
		f.runtime.registerTarget("hub", "other-session", new FakeClient("closed"), {
			threadId: "other-thread",
			instanceId: "other-instance",
		});
		await f.runtime.submit(
			request({ targetRef: "other-session", threadId: "other-thread", instanceId: "other-instance" }),
		);
		const otherTarget = (await f.runtime.read(otherTargetKey)).outbox[0].clientMutationId;
		for (const id of [first, second]) expect(await f.runtime.editQueuedMessage(id, key)).toBeDefined();
		await f.runtime.storage.transferToRecovery(sibling, "rejected");
		expect(await f.runtime.editQueuedMessage(otherTarget, otherTargetKey)).toBeDefined();
		expect(await editQueuedMessageIntoDraft(f.runtime, key, first, f.document)).toBe("restored");
		expect(await editQueuedMessageIntoDraft(f.runtime, key, second, f.document)).toBe("restored");
		let commands = 0;
		await f.document.submitBuiltinCommand(async () => {
			commands++;
			return true;
		});
		const failId = cut === "first-delete" ? first : second;
		f.mutations.exec(`CREATE TRIGGER fail_cleanup BEFORE DELETE ON mutation_recovery
			WHEN OLD.client_mutation_id = '${failId}' BEGIN SELECT RAISE(ABORT, 'delete rejected'); END`);
		await expect(reconcileEditedResends(f.runtime, key, f.document)).rejects.toBeInstanceOf(Error);
		const afterFailure = (await f.runtime.read(key)).recovery.map((row) => row.clientMutationId);
		expect(afterFailure).toEqual(cut === "first-delete" ? [first, second, sibling] : [second, sibling]);
		expect((await f.runtime.read(otherTargetKey)).recovery.map((row) => row.clientMutationId)).toEqual([otherTarget]);
		await f.reopen();
		f.mutations.exec("DROP TRIGGER fail_cleanup");
		await reconcileEditedResends(f.runtime, key, f.document);
		expect((await f.runtime.read(key)).recovery.map((row) => row.clientMutationId)).toEqual([sibling]);
		expect((await f.runtime.read(otherTargetKey)).recovery.map((row) => row.clientMutationId)).toEqual([otherTarget]);
		expect(f.document.getSnapshot().record.completedCommandRecoveryIds).toBeUndefined();
		expect(commands).toBe(1);
		expect(f.client.calls).toEqual([]);
	},
);

test("all deletes survive marker-clear save failure and reopen treats absent IDs as complete", async () => {
	const f = fixture();
	const first = await f.enqueue();
	const second = await f.enqueue();
	for (const id of [first, second]) expect(await f.runtime.editQueuedMessage(id, key)).toBeDefined();
	for (const id of [first, second])
		expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
	let commands = 0;
	await f.document.submitBuiltinCommand(async () => {
		commands++;
		return true;
	});
	f.drafts.exec(`CREATE TRIGGER fail_marker_clear BEFORE DELETE ON drafts
		WHEN OLD.completed_command_recovery_ids IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'marker clear rejected'); END`);
	await reconcileEditedResends(f.runtime, key, f.document);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect(f.repository.read(destination).completedCommandRecoveryIds).toEqual([first, second]);
	expect(f.document.getSnapshot().record.completedCommandRecoveryIds).toEqual([first, second]);
	f.drafts.exec("DROP TRIGGER fail_marker_clear");
	await f.reopen();
	await reconcileEditedResends(f.runtime, key, f.document);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect(f.document.getSnapshot().record.completedCommandRecoveryIds).toBeUndefined();
	expect(commands).toBe(1);
});

test("finishing an older processed marker snapshot keeps newer cleanup IDs", () => {
	const f = fixture();
	f.repository.write(destination, { draft: "", unconfirmed: null, completedCommandRecoveryIds: ["processed"] });
	f.reloadDraft();
	f.repository.write(destination, {
		draft: "",
		unconfirmed: null,
		completedCommandRecoveryIds: ["processed", "newer"],
	});
	f.reloadDraft();
	expect(f.document.finishCompletedCommandRecovery(["processed"])).toBe(true);
	expect(f.repository.read(destination).completedCommandRecoveryIds).toEqual(["newer"]);
});

test("resumes a durable editing claim before append without reclaiming", async () => {
	const f = fixture();
	const id = await f.enqueue();
	expect(await f.runtime.editQueuedMessage(id, key)).toBeDefined();
	expect(f.document.getSnapshot().record.draft).toBe("");
	await f.reopen();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
	expect((await f.runtime.read(key)).recovery.map((r) => r.clientMutationId)).toEqual([id]);
	expect(f.client.calls).toEqual([]);
});

test("appends against latest work while the claim awaits and leaves a won claim intact if forgotten", async () => {
	const f = fixture();
	const a = await f.enqueue();
	const b = await f.enqueue();
	const editing = editQueuedMessageIntoDraft(f.runtime, key, a, f.document);
	f.document.edit("newer");
	expect(await editing).toBe("restored");
	expect(f.document.getSnapshot().record.draft).toBe("newer\n\nsame");
	const unsubscribe = f.runtime.subscribeStorage(() => f.document.forget());
	expect(await editQueuedMessageIntoDraft(f.runtime, key, b, f.document)).toBe("blocked");
	unsubscribe();
	expect((await f.runtime.read(key)).recovery.map((r) => r.clientMutationId)).toEqual([a, b]);
	await f.reopen();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, b, f.document)).toBe("restored");
	expect(f.document.getSnapshot().record.draft).toBe("newer\n\nsame\n\nsame");
});

test("failed draft and mutation writes retain owners and recover after storage repair", async () => {
	const f = fixture();
	const id = await f.enqueue();
	f.document.edit("current");
	f.mutations.exec("PRAGMA query_only = ON");
	await expect(editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).rejects.toBeInstanceOf(Error);
	f.mutations.exec("PRAGMA query_only = OFF");
	expect((await f.runtime.read(key)).outbox.map((r) => r.clientMutationId)).toEqual([id]);
	f.drafts.exec("PRAGMA query_only = ON");
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("blocked");
	expect(f.repository.read(destination).draft).toBe("current");
	expect((await f.runtime.read(key)).recovery.map((r) => r.clientMutationId)).toEqual([id]);
	f.drafts.exec("PRAGMA query_only = OFF");
	f.document.retry();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
	await f.reopen();
	expect(f.document.getSnapshot().record.draft).toBe("current\n\nsame");
});

test("unavailable documents do not claim and losing claims do not copy", async () => {
	const f = fixture();
	const id = await f.enqueue();
	f.document.forget();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("blocked");
	expect((await f.runtime.read(key)).outbox.map((r) => r.clientMutationId)).toEqual([id]);
	await f.reopen();
	const editing = editQueuedMessageIntoDraft(f.runtime, key, id, f.document);
	await f.runtime.storage.markAttempted(id);
	expect(await editing).toBe("lost");
	expect(f.document.getSnapshot().record.draft).toBe("");
	expect(f.client.calls).toEqual([]);
});

test("wrong-document Edit does not claim or retire the selected source", async () => {
	const f = fixture();
	const id = await f.enqueue();
	const other = new DraftDocument(() => f.repository, { ...destination, sessionRef: "other" });
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, other)).toBe("blocked");
	expect((await f.runtime.read(key)).outbox.map((r) => r.clientMutationId)).toEqual([id]);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect(other.getSnapshot().record.draft).toBe("");
});

type Fixture = ReturnType<typeof fixture>;
const sourceImage: InputAttachment = { marker: 9, mediaType: "image/png", name: "source.png", data: "AP8H" };
async function richSource(f: Fixture) {
	const text = "[image 9] /skill";
	const composer = {
		text,
		attachments: [sourceImage],
		skillNames: ["skill"],
		commandNames: ["command"],
		mentions: [{ kind: "skill" as const, name: "skill", offset: 10 }],
	};
	return f.enqueue({
		input: buildComposerInput(text, [sourceImage], composer.skillNames, composer.commandNames, composer.mentions),
		composer,
	});
}
async function explicitSend(f: Fixture) {
	await f.document.submit(async (text, images, context) => {
		expect(f.repository.read(destination).unconfirmed).toBe(text);
		expect(f.repository.read(destination).unconfirmedCaptureToken).toBe(context.draftCaptureToken);
		expect(f.repository.read(destination).unconfirmedComposer?.recoveryIds).toEqual(context.recoverySourceIds);
		await f.runtime.submit(
			request({
				input: buildComposerInput(
					text,
					images,
					context.composer?.skillNames,
					context.composer?.commandNames,
					context.composer?.mentions,
				),
				...context,
			}),
		);
		const receipt = await f.runtime.readRecoveryResend(key, context.recoverySourceIds ?? []);
		expect(receipt).toBeDefined();
		const outbox = (await f.runtime.read(key)).outbox;
		const admitted = outbox.filter((row) => row.clientMutationId === receipt!.clientMutationId);
		expect(admitted).toHaveLength(1);
		expect(admitted[0].clientMutationId).toBe(receipt!.clientMutationId);
		expect(admitted[0].draftCaptureToken).toBe(context.draftCaptureToken);
		return true;
	});
}
function newerWork(f: Fixture) {
	f.document.edit("newer /later");
	f.document.addImage({ id: "newer-image", marker: 20, mediaType: "image/png", data: "AQID" });
	f.repository.write(destination, {
		...f.document.getSnapshot().record,
		composer: {
			skillNames: ["later"],
			commandNames: ["later-command"],
			mentions: [{ kind: "skill", name: "later", offset: 6 }],
		},
	});
	f.reloadDraft();
	return structuredClone(f.document.getSnapshot().record);
}
async function connectAndSettle(f: Fixture) {
	f.connect();
	f.client.on(
		"turn/start",
		(params) =>
			({
				receipt: {
					clientMutationId: params.clientMutationId,
					disposition: "applied",
					threadId: "thread",
					turnId: "turn",
					projectionState: "pending",
				},
				turn: { id: "turn" },
			}) as never,
	);
	f.client.emitStateChange("ready");
	await f.runtime.start();
	const lease = f.runtime.beginAuthoritativeRead("hub", "session", f.client);
	expect(lease).toBeDefined();
	await f.runtime.reconcileAuthoritativeRead(lease!, {
		thread: {
			id: "thread",
			status: { type: "idle" },
			evener: {
				queue: { revision: 0, clientMutationIds: [] },
				ref: "session",
				instanceId: "instance",
				mutationStateAuthoritative: true,
			},
		},
	} as unknown as import("@evener/appwire-client").ThreadReadResponse);
	// Dispatch runs asynchronously and exposes storage notifications rather than a completion promise.
	await vi.waitFor(async () => expect((await f.runtime.read(key)).outbox).toEqual([]));
	f.client.emitStateChange("closed");
}
const cuts = [
	"edit claim",
	"draft append",
	"resend admission",
	"outbox settlement",
	"draft confirmation",
	"mutation receipt acknowledgment",
	"draft acknowledgment-marker deletion",
] as const;
test.each(cuts)("editedResendSurvivesEveryDatabaseCutPoint: %s", async (cut) => {
	const f = fixture();
	const a = await richSource(f);
	const b = await richSource(f);
	const removed = await richSource(f);
	expect(await f.runtime.removeQueuedMessage(removed, key)).toBe(true);
	for (const id of [a, b]) expect(await f.runtime.editQueuedMessage(id, key)).toBeDefined();
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	expect((await f.runtime.read(key)).recovery.map((r) => r.clientMutationId)).toEqual([a, b]);
	expect(f.document.getSnapshot().record.composer).toBeUndefined();
	if (cut === "edit claim") await f.reopen();
	for (const id of [a, b]) expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([a, b]);
	expect(f.document.getSnapshot().record.composer).toEqual({
		recoveryIds: [a, b],
		skillNames: ["skill"],
		commandNames: ["command"],
		mentions: [
			{ kind: "skill", name: "skill", offset: 10 },
			{ kind: "skill", name: "skill", offset: 28 },
		],
	});
	expect(f.document.imagePreviews().map((i) => i.data)).toEqual(["AP8H", "AP8H"]);
	if (cut === "draft append") await f.reopen();
	f.document.edit(`${f.document.getSnapshot().record.draft} revised`);
	const captured = structuredClone(f.document.getSnapshot().record);
	expect(f.client.calls).toEqual([]);
	await explicitSend(f);
	const receipt = await f.runtime.readRecoveryResend(key, [b, a]);
	expect(receipt).toEqual({ targetRef: key, sourceIds: [a, b], clientMutationId: "mutation-4" });
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect((await f.runtime.read(key)).outbox.map((r) => r.clientMutationId)).toEqual(["mutation-4"]);
	expect(f.document.getSnapshot().record.unconfirmed).toBe(captured.draft);
	const captureToken = f.repository.read(destination).unconfirmedCaptureToken;
	expect(captureToken).toEqual(expect.any(String));
	expect((await f.runtime.read(key)).outbox[0].draftCaptureToken).toBe(captureToken);
	expect(f.document.getSnapshot().record.unconfirmedComposer).toEqual(captured.composer);
	expect(f.document.imagePreviews(f.document.getSnapshot().record.unconfirmedImages).map((i) => i.data)).toEqual([
		"AP8H",
		"AP8H",
	]);
	const newer = newerWork(f);
	if (cut === "resend admission") await f.reopen();
	await connectAndSettle(f);
	expect(f.client.calls.map((c) => c.params)).toEqual([expect.objectContaining({ clientMutationId: "mutation-4" })]);
	expect(f.client.calls[0].params).toMatchObject({
		input: expect.arrayContaining([
			{ type: "image", mediaType: "image/png", name: "source.png", data: "AP8H" },
			{ type: "skill", name: "skill" },
			{ type: "command", name: "command" },
		]),
	});
	expect(await f.runtime.readRecoveryResend(key, [a, b])).toEqual(receipt);
	expect(f.document.getSnapshot().record.unconfirmed).toBe(captured.draft);
	if (cut === "outbox settlement") await f.reopen();
	if (cut === "draft confirmation") {
		f.mutations.exec("PRAGMA query_only = ON");
		await expect(reconcileEditedResends(f.runtime, key, f.document)).rejects.toBeInstanceOf(Error);
		expect(f.repository.read(destination).unconfirmed).toBeNull();
		expect(f.repository.read(destination).unconfirmedCaptureToken).toBeUndefined();
		expect(f.repository.read(destination).recoveryResendAcks).toEqual(["mutation-4"]);
		expect(await f.runtime.readRecoveryResend(key, [a, b])).toEqual(receipt);
		await f.reopen();
	} else if (cut === "mutation receipt acknowledgment") {
		const unsubscribe = f.runtime.subscribeStorage(() => f.drafts.exec("PRAGMA query_only = ON"));
		await reconcileEditedResends(f.runtime, key, f.document);
		unsubscribe();
		expect(await f.runtime.readRecoveryResend(key, [a, b])).toBeUndefined();
		expect(f.repository.read(destination).unconfirmed).toBeNull();
		expect(f.repository.read(destination).unconfirmedCaptureToken).toBeUndefined();
		expect(f.repository.read(destination).recoveryResendAcks).toEqual(["mutation-4"]);
		await f.reopen();
	}
	await reconcileEditedResends(f.runtime, key, f.document);
	expect(f.repository.read(destination).recoveryResendAcks).toBeUndefined();
	if (cut === "draft acknowledgment-marker deletion") await f.reopen();
	await Promise.all([
		reconcileEditedResends(f.runtime, key, f.document),
		reconcileEditedResends(f.runtime, key, f.document),
	]);
	expect(f.document.getSnapshot().record).toEqual({
		...newer,
		unconfirmed: null,
		unconfirmedCaptureToken: undefined,
		unconfirmedImages: undefined,
		unconfirmedComposer: undefined,
	});
	expect(f.document.imagePreviews().map((i) => i.data)).toEqual(["AQID"]);
	expect(await f.runtime.readRecoveryResend(key, [a, b])).toBeUndefined();
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect(await editQueuedMessageIntoDraft(f.runtime, key, a, f.document)).toBe("lost");
	await expect(f.runtime.submit(request({ recoverySourceIds: [a, b] }))).rejects.toBeInstanceOf(Error);
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	await f.reopen();
	await connectAndSettle(f);
	expect(f.calls.map((c) => c.params)).toEqual([expect.objectContaining({ clientMutationId: "mutation-4" })]);
});

test("retained exact sources restore the edited captured revision beside newer work without automatic resend", async () => {
	const f = fixture();
	const a = await richSource(f);
	const b = await richSource(f);
	for (const id of [a, b]) await editQueuedMessageIntoDraft(f.runtime, key, id, f.document);
	f.document.edit("revision [image 1] [image 2]");
	f.mutations.exec("PRAGMA query_only = ON");
	await expect(explicitSend(f)).rejects.toBeInstanceOf(Error);
	f.mutations.exec("PRAGMA query_only = OFF");
	expect(f.document.getSnapshot().record.unconfirmed).toBe("revision [image 1] [image 2]");
	expect((await f.runtime.read(key)).recovery.map((r) => r.clientMutationId)).toEqual([a, b]);
	newerWork(f);
	await f.reopen();
	await Promise.all([
		reconcileEditedResends(f.runtime, key, f.document),
		reconcileEditedResends(f.runtime, key, f.document),
	]);
	expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
	expect(f.document.getSnapshot().record.draft).toBe("newer /later[image 20]\n\nrevision [image 1] [image 2]");
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toEqual([a, b]);
	expect(f.document.imagePreviews().map((i) => i.data)).toEqual(["AQID", "AP8H", "AP8H"]);
	expect(f.client.calls).toEqual([]);
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	await explicitSend(f);
	await reconcileEditedResends(f.runtime, key, f.document);
	expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
	expect((await f.runtime.read(key)).outbox).toHaveLength(1);
});

test("wrong-target reconciliation cannot settle captured ownership or acknowledge persisted markers", async () => {
	const f = fixture();
	const id = await richSource(f);
	await editQueuedMessageIntoDraft(f.runtime, key, id, f.document);
	await explicitSend(f);
	const receipt = await f.runtime.readRecoveryResend(key, [id]);
	const before = structuredClone(f.document.getSnapshot().record);
	const otherKey = nativeMutationTargetKey("other-hub", "session");
	await reconcileEditedResends(f.runtime, otherKey, f.document);
	expect(f.document.getSnapshot().record).toEqual(before);
	expect(await f.runtime.readRecoveryResend(key, [id])).toEqual(receipt);
	expect(f.document.settleEditedResend(receipt!)).toBe(true);
	await reconcileEditedResends(f.runtime, otherKey, f.document);
	expect(f.document.getSnapshot().record.recoveryResendAcks).toEqual([receipt!.clientMutationId]);
	expect(await f.runtime.readRecoveryResend(key, [id])).toEqual(receipt);
	await reconcileEditedResends(f.runtime, key, f.document);
	expect(f.document.getSnapshot().record.recoveryResendAcks).toBeUndefined();
	expect(await f.runtime.readRecoveryResend(key, [id])).toBeUndefined();
});

test("partial receipts and incomplete retained sources never prove an absent outbox capture safe", async () => {
	const f = fixture();
	const a = await richSource(f);
	const b = await richSource(f);
	for (const id of [a, b]) await editQueuedMessageIntoDraft(f.runtime, key, id, f.document);
	// Real draft capture and real admission of only one source produce a nonmatching receipt.
	await f.document.submit(async (text, images, context) => {
		await f.runtime.submit(request({ input: buildComposerInput(text, images), ...context, recoverySourceIds: [a] }));
		return true;
	});
	const partial = await f.runtime.readRecoveryResend(key, [a]);
	expect(partial).toBeDefined();
	await f.runtime.settleIdentities([partial!.clientMutationId]);
	const before = structuredClone(f.document.getSnapshot().record);
	await f.reopen();
	await reconcileEditedResends(f.runtime, key, f.document);
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	expect((await f.runtime.read(key)).recovery.map((r) => r.clientMutationId)).toEqual([b]);
	expect(f.document.getSnapshot().record).toEqual(before);
	expect(await f.runtime.readRecoveryResend(key, [a])).toEqual(partial);
	// No retained source at all is still not proof. Neither observation resends.
	await f.runtime.discardRecovery(b, key);
	await reconcileEditedResends(f.runtime, key, f.document);
	expect(f.document.getSnapshot().record).toEqual(before);
	expect(f.client.calls).toEqual([]);
});

test.each(["confirmation", "restoration"] as const)(
	"failed draft %s preserves captured bytes until retry succeeds",
	async (phase) => {
		const f = fixture();
		const id = await richSource(f);
		await editQueuedMessageIntoDraft(f.runtime, key, id, f.document);
		if (phase === "confirmation") await explicitSend(f);
		else {
			f.document.replaceDraft("replacement");
			// The replacement itself is not a restore: clear it before explicitly
			// bringing the parked composition back.
			f.document.edit("");
		}
		const before = structuredClone(f.document.getSnapshot().record);
		f.drafts.exec("PRAGMA query_only = ON");
		if (phase === "confirmation") await reconcileEditedResends(f.runtime, key, f.document);
		else f.document.restoreParked(before.parkedCompositions?.[0]?.id ?? "");
		expect(f.document.getSnapshot().error).not.toBeNull();
		expect(f.repository.read(destination)).toEqual(before);
		expect(
			f.document
				.imagePreviews(phase === "confirmation" ? before.unconfirmedImages : before.parkedCompositions?.[0]?.images)
				.map((i) => i.data),
		).toEqual(["AP8H"]);
		if (phase === "confirmation") expect(await f.runtime.readRecoveryResend(key, [id])).toBeDefined();
		else expect((await f.runtime.read(key)).recovery.map((r) => r.clientMutationId)).toEqual([id]);
		f.drafts.exec("PRAGMA query_only = OFF");
		// Changes made during failed persistence survive the eventual retry too.
		if (phase === "confirmation") f.document.edit("latest");
		f.document.retry();
		if (phase === "confirmation") await reconcileEditedResends(f.runtime, key, f.document);
		else f.document.restoreParked(before.parkedCompositions?.[0]?.id ?? "");
		expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
		expect(f.document.getSnapshot().record.draft).toBe(phase === "confirmation" ? "latest" : "[image 1] /skill");
		expect(f.document.getSnapshot().record.recoveryResendAcks).toBeUndefined();
		expect(f.client.calls).toEqual([]);
	},
);

test.each(["mutation_sequence", "mutation_recovery"] as const)(
	"failed fresh read of %s leaves recovery intact and a later call completes",
	async (table) => {
		const f = fixture();
		const id = await richSource(f);
		await editQueuedMessageIntoDraft(f.runtime, key, id, f.document);
		if (table === "mutation_sequence") await explicitSend(f);
		else {
			f.document.edit("parked revision");
			// A failed-send capture still reconciles through retained sources; a
			// deliberate replacement parks its work apart and never reads them.
			const current = f.document.getSnapshot().record;
			f.repository.write(destination, {
				draft: "replacement",
				unconfirmed: current.draft,
				unconfirmedImages: current.images,
				unconfirmedComposer: current.composer,
			});
			f.reloadDraft();
		}
		const before = structuredClone(f.document.getSnapshot().record);
		// SQLite itself rejects the read, not an application method replacement.
		f.mutations.exec(`ALTER TABLE ${table} RENAME TO unavailable_table`);
		try {
			await expect(reconcileEditedResends(f.runtime, key, f.document)).rejects.toBeInstanceOf(Error);
		} finally {
			f.mutations.exec(`ALTER TABLE unavailable_table RENAME TO ${table}`);
		}
		expect(f.repository.read(destination)).toEqual(before);
		await reconcileEditedResends(f.runtime, key, f.document);
		expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
		expect(f.document.getSnapshot().record.draft).toBe(
			table === "mutation_sequence" ? "" : "replacement\n\nparked revision",
		);
		expect(f.client.calls).toEqual([]);
	},
);

test("a fresh retained-source read observes removal while reconciliation is queued", async () => {
	const f = fixture();
	const id = await richSource(f);
	await editQueuedMessageIntoDraft(f.runtime, key, id, f.document);
	// The capture must exist for reconciliation to read retained sources at all.
	await f.document.submit(async () => false);
	const before = structuredClone(f.document.getSnapshot().record);
	expect(before.unconfirmedComposer?.recoveryIds).toEqual([id]);
	const pending = reconcileEditedResends(f.runtime, key, f.document);
	expect(await f.runtime.discardRecovery(id, key)).toBe(true);
	await pending;
	expect(f.document.getSnapshot().record).toEqual(before);
	// The removed source proves nothing about admission, so the captured
	// composition stays intact instead of being restored or cleared.
	expect(f.document.getSnapshot().record.unconfirmed).toBe(before.unconfirmed);
	expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toEqual([id]);
	expect((await f.runtime.read(key)).recovery).toEqual([]);
	expect(f.client.calls).toEqual([]);
});

test("reconciliation waits for an active explicit submit instead of restoring its retained sources", async () => {
	const f = fixture();
	const id = await richSource(f);
	await editQueuedMessageIntoDraft(f.runtime, key, id, f.document);
	let release!: () => void;
	const gate = new Promise<void>((resolve) => {
		release = resolve;
	});
	const sending = f.document.submit(async (text, images, context) => {
		await gate;
		await f.runtime.submit(request({ input: buildComposerInput(text, images), ...context }));
		return true;
	});
	const captured = f.document.getSnapshot().record.unconfirmed;
	f.document.edit("while awaiting explicit submission");
	await reconcileEditedResends(f.runtime, key, f.document);
	expect(f.document.getSnapshot().record.unconfirmed).toBe(captured);
	expect((await f.runtime.read(key)).recovery.map((r) => r.clientMutationId)).toEqual([id]);
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("blocked");
	release();
	await sending;
	await reconcileEditedResends(f.runtime, key, f.document);
	expect(f.document.getSnapshot().record.draft).toBe("while awaiting explicit submission");
	expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
	expect((await f.runtime.read(key)).outbox).toHaveLength(1);
	expect(f.client.calls).toEqual([]);
});

test("old-native gapped literal markers stay unbound beside current images across recovery and resend", async () => {
	const f = fixture();
	const legacy = await f.enqueue({
		input: [
			{ type: "text", text: "literal [image 9] gap [image 3]" },
			{ type: "image", mediaType: "image/png", data: "AP8H" },
		],
	});
	await f.reopen();
	f.document.edit("[image 1] existing");
	f.document.addImage({ id: "current", marker: 9, mediaType: "image/png", data: "AQID" });
	expect(await editQueuedMessageIntoDraft(f.runtime, key, legacy, f.document)).toBe("restored");
	expect(f.document.getSnapshot().record.draft).toBe("[image 1] existing[image 2]\n\nliteral [image 9] gap [image 3]");
	expect(f.document.imagePreviews().map((i) => [i.marker, i.data])).toEqual([
		[2, "AQID"],
		[4, "AP8H"],
	]);
	await f.reopen();
	const later = await richSource(f);
	expect(await editQueuedMessageIntoDraft(f.runtime, key, later, f.document)).toBe("restored");
	expect(f.document.getSnapshot().record.draft).toBe(
		"[image 1] existing[image 2]\n\nliteral [image 9] gap [image 3]\n\n[image 5] /skill",
	);
	expect(f.document.imagePreviews().map((i) => [i.marker, i.data])).toEqual([
		[2, "AQID"],
		[4, "AP8H"],
		[5, "AP8H"],
	]);
	await explicitSend(f);
	await f.reopen();
	await reconcileEditedResends(f.runtime, key, f.document);
	expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
	const [row] = (await f.runtime.read(key)).outbox;
	expect(
		(row.payload.input as Array<{ type: string; data?: string }>).filter((i) => i.type === "image").map((i) => i.data),
	).toEqual(["AQID", "AP8H", "AP8H"]);
	expect(f.client.calls).toEqual([]);
});

test("ordinary uncertainty and rejected recovery are not edited or automatically replayed", async () => {
	const f = fixture();
	const id = await f.enqueue();
	await f.runtime.storage.transferToRecovery(id, "rejected");
	f.document.edit("ordinary");
	await f.document.submit(async (text) => {
		await f.runtime.submit(request({ input: [{ type: "text", text }] }));
		return false;
	});
	const before = structuredClone(f.document.getSnapshot().record);
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("lost");
	await reconcileEditedResends(f.runtime, key, f.document);
	expect(f.document.getSnapshot().record).toEqual(before);
	expect((await f.runtime.read(key)).recovery.map((r) => [r.clientMutationId, r.recoveryKind])).toEqual([
		[id, "rejected"],
	]);
	expect(f.client.calls).toEqual([]);
});

test("stored acknowledgments finish before reconciling a later capture and preserve newer typing", async () => {
	const f = fixture();
	const sources: string[] = [];
	const receipts: string[] = [];
	for (let index = 0; index < 3; index++) {
		const id = await f.enqueue();
		sources.push(id);
		expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
		await explicitSend(f);
		const receipt = await f.runtime.readRecoveryResend(key, [id]);
		expect(receipt).toBeDefined();
		receipts.push(receipt!.clientMutationId);
		if (index < 2) expect(f.document.settleEditedResend(receipt!)).toBe(true);
	}
	expect(f.document.getSnapshot().record.recoveryResendAcks).toEqual(receipts.slice(0, 2));
	expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toEqual([sources[2]]);
	await f.reopen();
	const unsubscribe = f.runtime.subscribeStorage(() => f.document.edit("typed while acknowledging"));
	await reconcileEditedResends(f.runtime, key, f.document);
	unsubscribe();
	expect(f.document.getSnapshot().record.draft).toBe("typed while acknowledging");
	expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
	expect(f.document.getSnapshot().record.recoveryResendAcks).toBeUndefined();
	for (const id of sources) expect(await f.runtime.readRecoveryResend(key, [id])).toBeUndefined();
	expect((await f.runtime.read(key)).outbox.map((r) => r.clientMutationId)).toEqual(receipts);
	expect(f.calls).toEqual([]);
});

test.each(["image", "skill", "command"] as const)(
	"%s-only edited input captures and reconciles without a text gate",
	async (kind) => {
		const f = fixture();
		const attachments = kind === "image" ? [sourceImage] : [];
		const skillNames = kind === "skill" ? ["selected"] : [];
		const commandNames = kind === "command" ? ["selected"] : [];
		const id = await f.enqueue({
			input: buildComposerInput("", attachments, skillNames, commandNames),
			composer: { text: "", attachments, skillNames, commandNames },
		});
		expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
		expect(f.document.getSnapshot().record.draft).toBe("");
		await explicitSend(f);
		expect(f.document.getSnapshot().record.unconfirmed).toBe("");
		expect(f.document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toEqual([id]);
		await f.reopen();
		await reconcileEditedResends(f.runtime, key, f.document);
		expect(f.document.getSnapshot().record.unconfirmed).toBeNull();
		expect(f.document.getSnapshot().record.recoveryResendAcks).toBeUndefined();
		const rows = (await f.runtime.read(key)).outbox;
		expect(rows).toHaveLength(1);
		expect(rows[0].clientMutationId).not.toBe(id);
		expect(rows[0].payload.input).toEqual(
			kind === "image"
				? [{ type: "image", mediaType: "image/png", name: "source.png", data: "AP8H" }]
				: [{ type: kind, name: "selected" }],
		);
		expect(f.calls).toEqual([]);
	},
);

test("ordinary enqueue followed by failed draft confirmation and reopen clears duplicate on queued Edit", async () => {
	const f = fixture();
	f.document.edit("ordinary queued message");
	f.drafts.exec(`CREATE TRIGGER reject_confirmation BEFORE DELETE ON drafts
		WHEN OLD.unconfirmed IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'draft confirmation rejected'); END`);
	await expect(
		f.document.submit(async (text, _images, context) => {
			await f.runtime.submit({ ...request({ input: [{ type: "text", text }] }), ...context });
			return true;
		}),
	).rejects.toBeInstanceOf(Error);
	const [queued] = (await f.runtime.read(key)).outbox;
	expect(queued).toMatchObject({
		targetRef: key,
		payload: { input: [{ type: "text", text: "ordinary queued message" }] },
	});
	expect(queued.payload).not.toHaveProperty("draftCaptureToken");
	expect(f.repository.read(destination)).toMatchObject({ draft: "", unconfirmed: "ordinary queued message" });
	f.drafts.exec("DROP TRIGGER reject_confirmation");

	await f.reopen();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, queued.clientMutationId, f.document)).toBe("restored");
	const [recovery] = (await f.runtime.read(key)).recovery;
	expect(recovery).toMatchObject({
		clientMutationId: queued.clientMutationId,
		recoveryKind: "editing",
		payload: { input: [{ type: "text", text: "ordinary queued message" }] },
	});
	const afterEdit = f.repository.read(destination);
	expect(afterEdit).toMatchObject({
		draft: "ordinary queued message",
		composer: { recoveryIds: [queued.clientMutationId] },
		unconfirmed: null,
	});
	expect(queued.draftCaptureToken).toEqual(expect.any(String));
	expect(recovery?.draftCaptureToken).toBe(queued.draftCaptureToken);
	expect(f.calls).toEqual([]);
});

test("ordinary enqueue followed by failed draft confirmation and reopen clears duplicate on queued Remove", async () => {
	const f = fixture();
	f.document.edit("ordinary queued message");
	f.drafts.exec(`CREATE TRIGGER reject_confirmation BEFORE DELETE ON drafts
		WHEN OLD.unconfirmed IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'draft confirmation rejected'); END`);
	await expect(
		f.document.submit(async (text, _images, context) => {
			await f.runtime.submit({ ...request({ input: [{ type: "text", text }] }), ...context });
			return true;
		}),
	).rejects.toBeInstanceOf(Error);
	const [queued] = (await f.runtime.read(key)).outbox;
	expect(queued.draftCaptureToken).toEqual(expect.any(String));
	expect(f.repository.read(destination)).toMatchObject({
		draft: "",
		unconfirmed: "ordinary queued message",
		unconfirmedCaptureToken: queued.draftCaptureToken,
	});
	f.drafts.exec("DROP TRIGGER reject_confirmation");

	await f.reopen();
	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, queued.clientMutationId, f.document)).toBe(true);
	expect((await f.runtime.read(key)).outbox).toEqual([]);
	expect(f.repository.read(destination)).toMatchObject({ draft: "", unconfirmed: null });
	expect(f.repository.read(destination).unconfirmedCaptureToken).toBeUndefined();
	expect(f.calls).toEqual([]);
});

test("matching Remove keeps the row when durable capture cannot be cleared before retry or reopen", async () => {
	const f = fixture();
	f.document.edit("ordinary queued message");
	f.drafts.exec(`CREATE TRIGGER reject_confirmation BEFORE DELETE ON drafts
		WHEN OLD.unconfirmed IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'draft confirmation rejected'); END`);
	await expect(
		f.document.submit(async (text, _images, context) => {
			await f.runtime.submit({ ...request({ input: [{ type: "text", text }] }), ...context });
			return true;
		}),
	).rejects.toBeInstanceOf(Error);
	const [queued] = (await f.runtime.read(key)).outbox;
	expect(queued.draftCaptureToken).toEqual(expect.any(String));
	expect(f.document.getSnapshot().record).toMatchObject({ unconfirmed: null });
	expect(f.document.getSnapshot().record.unconfirmedCaptureToken).toBeUndefined();
	expect(f.repository.read(destination)).toMatchObject({
		unconfirmed: "ordinary queued message",
		unconfirmedCaptureToken: queued.draftCaptureToken,
	});

	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, queued.clientMutationId, f.document)).toBe(false);
	expect(await f.runtime.storage.getOutbox(queued.clientMutationId)).toMatchObject({
		draftCaptureToken: queued.draftCaptureToken,
	});
	expect(f.repository.read(destination)).toMatchObject({
		unconfirmed: "ordinary queued message",
		unconfirmedCaptureToken: queued.draftCaptureToken,
	});

	await f.reopen();
	expect(f.document.getSnapshot().record).toMatchObject({
		unconfirmed: "ordinary queued message",
		unconfirmedCaptureToken: queued.draftCaptureToken,
	});
	expect(await f.runtime.storage.getOutbox(queued.clientMutationId)).toBeDefined();
	f.drafts.exec("DROP TRIGGER reject_confirmation");
	f.document.edit("newer composition");
	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, queued.clientMutationId, f.document)).toBe(true);
	expect(await f.runtime.storage.getOutbox(queued.clientMutationId)).toBeUndefined();
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer composition",
		unconfirmed: null,
	});
	expect(f.repository.read(destination).unconfirmedCaptureToken).toBeUndefined();
});

test("matching capture preclear preserves newer composition and cleanup markers", async () => {
	const f = fixture();
	const first = await f.enqueue({ draftCaptureToken: "capture-first" });
	const sibling = await f.enqueue({ draftCaptureToken: "capture-sibling" });
	f.repository.write(
		destination,
		{
			draft: "newer typing[image 8]",
			unconfirmed: "older uncertain composition",
			images: [{ id: "newer-image", marker: 8, mediaType: "image/png" }],
			unconfirmedImages: [{ id: "old-image", marker: 9, mediaType: "image/png" }],
			composer: {
				skillNames: ["newer-skill"],
				mentions: [{ kind: "skill", name: "newer-skill", offset: 0 }],
				recoveryIds: ["newer-recovery"],
			},
			unconfirmedComposer: { commandNames: ["older-command"], recoveryIds: ["older-recovery"] },
			unconfirmedCaptureToken: "capture-first",
			recoveryResendAcks: ["pending-ack"],
			completedCommandRecoveryIds: ["completed-command"],
		},
		[
			{ id: "newer-image", marker: 8, mediaType: "image/png", data: "AQID" },
			{ id: "old-image", marker: 9, mediaType: "image/png", data: "BAUG" },
		],
	);
	f.reloadDraft();

	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, first, f.document)).toBe(true);
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer typing[image 8]",
		unconfirmed: null,
		images: [{ id: "newer-image", marker: 8 }],
		composer: {
			skillNames: ["newer-skill"],
			recoveryIds: ["newer-recovery"],
		},
		recoveryResendAcks: ["pending-ack"],
		completedCommandRecoveryIds: ["completed-command"],
	});
	expect(f.repository.read(destination).unconfirmedCaptureToken).toBeUndefined();
	expect(await f.runtime.storage.getOutbox(first)).toBeUndefined();
	expect(await f.runtime.storage.getOutbox(sibling)).toMatchObject({ draftCaptureToken: "capture-sibling" });
});

test("identical queued payloads clear only the capture token owned by the selected ID", async () => {
	const f = fixture();
	const first = await f.enqueue({ draftCaptureToken: "capture-first" });
	const second = await f.enqueue({ draftCaptureToken: "capture-second" });
	const rows = (await f.runtime.read(key)).outbox;
	expect(first).not.toBe(second);
	expect(rows[0].payload.input).toEqual(rows[1].payload.input);
	f.repository.write(destination, {
		draft: "newer draft",
		unconfirmed: "same",
		unconfirmedCaptureToken: "capture-second",
	});
	f.reloadDraft();

	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, first, f.document)).toBe(true);
	expect(await f.runtime.storage.getOutbox(first)).toBeUndefined();
	expect(await f.runtime.storage.getOutbox(second)).toMatchObject({ draftCaptureToken: "capture-second" });
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer draft",
		unconfirmed: "same",
		unconfirmedCaptureToken: "capture-second",
	});
});

test.each([
	{ name: "different token", rowToken: "row-capture", currentToken: "newer-capture" },
	{ name: "legacy missing token", rowToken: undefined, currentToken: undefined },
])("$name leaves uncertainty untouched without blocking Edit", async ({ rowToken, currentToken }) => {
	const f = fixture();
	const id = await f.enqueue({ draftCaptureToken: rowToken });
	f.repository.write(destination, {
		draft: "newer draft",
		unconfirmed: "uncertain copy",
		...(currentToken === undefined ? {} : { unconfirmedCaptureToken: currentToken }),
	});
	f.reloadDraft();

	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer draft\n\nsame",
		unconfirmed: "uncertain copy",
		...(currentToken === undefined ? {} : { unconfirmedCaptureToken: currentToken }),
	});
});

test.each([
	{ name: "different token", rowToken: "row-capture", currentToken: "newer-capture" },
	{ name: "legacy missing token", rowToken: undefined, currentToken: undefined },
])("$name leaves uncertainty untouched without blocking Remove", async ({ rowToken, currentToken }) => {
	const f = fixture();
	const id = await f.enqueue({ draftCaptureToken: rowToken });
	f.repository.write(destination, {
		draft: "newer draft",
		unconfirmed: "uncertain copy",
		...(currentToken === undefined ? {} : { unconfirmedCaptureToken: currentToken }),
	});
	f.reloadDraft();

	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, id, f.document)).toBe(true);
	expect(await f.runtime.storage.getOutbox(id)).toBeUndefined();
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer draft",
		unconfirmed: "uncertain copy",
		...(currentToken === undefined ? {} : { unconfirmedCaptureToken: currentToken }),
	});
});

test("failed matching capture preclear blocks queued Remove and leaves the row owner", async () => {
	const f = fixture();
	const id = await f.enqueue({ draftCaptureToken: "capture" });
	f.repository.write(destination, {
		draft: "",
		unconfirmed: "uncertain copy",
		unconfirmedCaptureToken: "capture",
	});
	f.reloadDraft();
	f.drafts.exec(`CREATE TRIGGER reject_capture_clear BEFORE DELETE ON drafts
		WHEN OLD.unconfirmed IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'capture clear rejected'); END`);

	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, id, f.document)).toBe(false);
	expect(await f.runtime.storage.getOutbox(id)).toMatchObject({ draftCaptureToken: "capture" });
	expect(f.repository.read(destination)).toMatchObject({
		unconfirmed: "uncertain copy",
		unconfirmedCaptureToken: "capture",
	});
});

test("a crash after matching preclear leaves the durable outbox row as owner", async () => {
	const f = fixture();
	const id = await f.enqueue({ draftCaptureToken: "capture" });
	f.repository.write(destination, {
		draft: "newer draft",
		unconfirmed: "uncertain copy",
		unconfirmedCaptureToken: "capture",
	});
	f.reloadDraft();
	const interruptedClaim = new Error("simulated process cut before claim");
	vi.spyOn(f.runtime, "editQueuedMessage").mockRejectedValue(interruptedClaim);

	await expect(editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).rejects.toBe(interruptedClaim);
	expect(f.repository.read(destination)).toMatchObject({ draft: "newer draft", unconfirmed: null });
	expect(await f.runtime.storage.getOutbox(id)).toMatchObject({ draftCaptureToken: "capture" });
});

test("a failed Edit append keeps the exact recovery owner across reopen", async () => {
	const f = fixture();
	const id = await f.enqueue({ draftCaptureToken: "capture" });
	f.repository.write(destination, {
		draft: "",
		unconfirmed: "same",
		unconfirmedCaptureToken: "capture",
	});
	f.reloadDraft();
	f.drafts.exec(`CREATE TRIGGER reject_append BEFORE INSERT ON drafts
		WHEN NEW.draft = 'same' BEGIN SELECT RAISE(ABORT, 'draft append rejected'); END`);

	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("blocked");
	expect(await f.runtime.storage.getOutbox(id)).toBeUndefined();
	expect(await f.runtime.storage.getRecovery(id)).toMatchObject({
		clientMutationId: id,
		recoveryKind: "editing",
		draftCaptureToken: "capture",
	});
	expect(f.repository.read(destination)).toMatchObject({ draft: "", unconfirmed: null });
	f.drafts.exec("DROP TRIGGER reject_append");

	await f.reopen();
	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("restored");
	expect(f.document.getSnapshot().record).toMatchObject({
		draft: "same",
		composer: { recoveryIds: [id] },
		unconfirmed: null,
	});
	expect(await f.runtime.storage.getRecovery(id)).toMatchObject({ draftCaptureToken: "capture" });
	expect(f.calls).toEqual([]);
});

test("a missing durable row cannot authorize clearing a matching uncertain capture", async () => {
	const f = fixture();
	f.repository.write(destination, {
		draft: "newer draft",
		unconfirmed: "uncertain copy",
		unconfirmedCaptureToken: "capture-without-row",
	});
	f.reloadDraft();

	expect(await editQueuedMessageIntoDraft(f.runtime, key, "missing-row", f.document)).toBe("lost");
	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, "missing-row", f.document)).toBe(false);
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer draft",
		unconfirmed: "uncertain copy",
		unconfirmedCaptureToken: "capture-without-row",
	});
});

test("tokenless Remove proceeds during an in-flight submission and preserves its uncertainty", async () => {
	const f = fixture();
	const id = await f.enqueue();
	f.document.edit("new submission");
	let beginSubmission!: () => void;
	let finishSubmission!: () => void;
	const submitting = new Promise<void>((resolve) => (beginSubmission = resolve));
	const finish = new Promise<void>((resolve) => (finishSubmission = resolve));
	const pendingSubmit = f.document.submit(async () => {
		beginSubmission();
		await finish;
		return false;
	});
	await submitting;
	expect(f.document.getSnapshot().submitting).toBe(true);

	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, id, f.document)).toBe(true);
	expect(await f.runtime.storage.getOutbox(id)).toBeUndefined();
	expect(f.repository.read(destination)).toMatchObject({
		unconfirmed: "new submission",
		unconfirmedCaptureToken: expect.any(String),
	});

	finishSubmission();
	await pendingSubmit;
});

test("different-token Remove proceeds with an errored composer and preserves uncertainty", async () => {
	const f = fixture();
	const id = await f.enqueue({ draftCaptureToken: "row-token" });
	f.repository.write(destination, {
		draft: "saved draft",
		unconfirmed: "uncertain composition",
		unconfirmedCaptureToken: "different-token",
	});
	f.reloadDraft();
	f.drafts.exec(`CREATE TRIGGER reject_unsaved_edit BEFORE INSERT ON drafts
		WHEN NEW.draft = 'unsaved edit' BEGIN SELECT RAISE(ABORT, 'injected draft failure'); END`);
	f.document.edit("unsaved edit");
	expect(f.document.getSnapshot().error).not.toBeNull();

	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, id, f.document)).toBe(true);
	expect(await f.runtime.storage.getOutbox(id)).toBeUndefined();
	expect(f.repository.read(destination)).toMatchObject({
		unconfirmed: "uncertain composition",
		unconfirmedCaptureToken: "different-token",
	});
});

test("matching capture Remove clears durably from an errored document", async () => {
	const f = fixture();
	const id = await f.enqueue({ draftCaptureToken: "matching-token" });
	f.repository.write(destination, {
		draft: "saved draft",
		unconfirmed: "uncertain composition",
		unconfirmedCaptureToken: "matching-token",
	});
	f.reloadDraft();
	f.drafts.exec(`CREATE TRIGGER reject_unsaved_edit BEFORE INSERT ON drafts
		WHEN NEW.draft = 'unsaved edit' BEGIN SELECT RAISE(ABORT, 'injected draft failure'); END`);
	f.document.edit("unsaved edit");
	expect(f.document.getSnapshot().error).not.toBeNull();
	f.drafts.exec("DROP TRIGGER reject_unsaved_edit");

	expect(await removeQueuedMessageWithDraftCapture(f.runtime, key, id, f.document)).toBe(true);
	expect(await f.runtime.storage.getOutbox(id)).toBeUndefined();
	expect(f.repository.read(destination)).toMatchObject({ draft: "unsaved edit", unconfirmed: null });
	expect(f.repository.read(destination).unconfirmedCaptureToken).toBeUndefined();
});

async function retainedEditingCapture(f: ReturnType<typeof fixture>) {
	const id = await f.enqueue({ draftCaptureToken: "retained-token" });
	expect(await f.runtime.editQueuedMessage(id, key)).toBeDefined();
	f.repository.write(destination, {
		draft: "newer draft",
		unconfirmed: "uncertain composition",
		unconfirmedCaptureToken: "retained-token",
	});
	f.reloadDraft();
	return id;
}

test("retained Edit rejects stale thread identity before preclear or append", async () => {
	const f = fixture();
	const id = await retainedEditingCapture(f);
	f.runtime.registerTarget("hub", "session", new FakeClient("closed"), {
		threadId: "replacement-thread",
		instanceId: "instance",
	});

	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("lost");
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer draft",
		unconfirmed: "uncertain composition",
		unconfirmedCaptureToken: "retained-token",
	});
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toBeUndefined();
	expect(await f.runtime.storage.getRecovery(id)).toMatchObject({ recoveryKind: "editing" });
});

test("retained Edit rejects stale instance identity before preclear or append", async () => {
	const f = fixture();
	const id = await retainedEditingCapture(f);
	f.runtime.registerTarget("hub", "session", new FakeClient("closed"), {
		threadId: "thread",
		instanceId: "replacement-instance",
	});

	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("lost");
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer draft",
		unconfirmed: "uncertain composition",
		unconfirmedCaptureToken: "retained-token",
	});
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toBeUndefined();
	expect(await f.runtime.storage.getRecovery(id)).toMatchObject({ recoveryKind: "editing" });
});

test("retained Edit rejects missing current identity before preclear or append", async () => {
	const f = fixture();
	const id = await retainedEditingCapture(f);
	f.unregisterTarget();

	expect(await editQueuedMessageIntoDraft(f.runtime, key, id, f.document)).toBe("lost");
	expect(f.repository.read(destination)).toMatchObject({
		draft: "newer draft",
		unconfirmed: "uncertain composition",
		unconfirmedCaptureToken: "retained-token",
	});
	expect(f.document.getSnapshot().record.composer?.recoveryIds).toBeUndefined();
	expect(await f.runtime.storage.getRecovery(id)).toMatchObject({ recoveryKind: "editing" });
});
