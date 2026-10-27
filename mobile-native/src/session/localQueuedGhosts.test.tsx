import { randomUUID } from "node:crypto";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, test, vi } from "vitest";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { View } from "react-native";
import { buildComposerInput } from "@evener/appwire-client";
import { createClientIdentity, reconcilePendingEntries } from "@evener/appwire-client/state/mutation";
import { DraftDocument } from "../draftDocument";
import { DraftRepository } from "../draftRepository";
import { projectNativeMutationRecovery } from "../MutationRecoveryPanel";
import { QueueSheet, queueHosts } from "../QueueSheet";
import { pressable, render, swipeRowFully, unmountMountedTrees } from "../renderNative.testkit";
import { sheetKey } from "../sheet/sheetHosts";
import { NativeMutationRuntime, nativeMutationTargetKey, type NativeMutationRequest } from "../nativeMutationRuntime";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import { GhostRowContext, GhostRowView } from "./GhostRows";
import { GhostBubble } from "./GhostBubble";
import { ghosts, isQueuedGhost, shownGhosts, type Ghost, type GhostAction, type GhostSource } from "./ghosts";
import { isGhostRow, withGhostRows } from "./transcriptRows";

const platform = vi.hoisted(() => ({ menu: vi.fn(), goBack: vi.fn(), dispatch: vi.fn() }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	ActionSheetIOS: { showActionSheetWithOptions: platform.menu },
}));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("@react-navigation/native", () => ({ useNavigation: () => platform, usePreventRemove: () => {} }));

vi.mock("expo-sqlite", () => ({ openDatabaseSync: vi.fn() }));
vi.mock("expo-crypto", async () => ({
	randomUUID: (await import("node:crypto")).randomUUID,
	getRandomValues: (array: Uint8Array) => array,
}));

const cleanups: Array<() => Promise<void>> = [];
afterEach(async () => {
	unmountMountedTrees();
	queueHosts.release(sheetKey("hub-1", "ref-1"), owner);
	vi.clearAllMocks();
	for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});
const owner = {};
const targetRef = nativeMutationTargetKey("hub-1", "ref-1");
const identity = createClientIdentity(undefined, { randomUUID });
const scope = { targetRef, threadId: "thread-1", instanceId: "instance-1" };
const ready = { document: { loaded: true, submitting: false, error: null }, encodingAttachments: false };
function fixture() {
	const directory = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "local-ghosts-"));
	const opened = openSqliteSyncDouble(join(directory, "mutations.sqlite"));
	let next = 0;
	const runtime = new NativeMutationRuntime(opened.port, {
		createMutationId: () => `mutation-${++next}`,
		getOwnClientId: identity.ownClientId,
	});
	cleanups.push(async () => {
		await runtime.stop();
		opened.database.close();
		rmSync(directory, { recursive: true });
	});
	return runtime;
}
async function submit(runtime: NativeMutationRuntime, extra: Partial<NativeMutationRequest> = {}) {
	await runtime.submit({
		hubId: "hub-1",
		targetRef: "ref-1",
		threadId: scope.threadId,
		instanceId: scope.instanceId,
		kind: "send",
		input: [{ type: "text", text: "same" }],
		...extra,
	} as NativeMutationRequest);
}
async function snapshot(runtime: NativeMutationRuntime) {
	const { outbox, optimistic } = await runtime.read(targetRef);
	const pending = reconcilePendingEntries(
		targetRef,
		[...outbox, ...optimistic],
		undefined,
		runtime.submittedHere(targetRef),
		identity.isOwnMutationRecord,
	);
	return { pending, local: { ...scope, records: outbox, readiness: ready } };
}

test("offline pending message offers local Edit and Remove from its scoped durable record", async () => {
	const runtime = fixture();
	await submit(runtime);
	const { pending, local } = await snapshot(runtime);
	expect(pending).toMatchObject([{ id: "mutation-1", state: "submitting", fromThisClient: true }]);
	expect(local.records).toMatchObject([{ clientMutationId: "mutation-1", attempted: false }]);
	const [ghost] = ghosts(null, pending, null, [], false, local);
	expect(ghost).toMatchObject({
		origin: { kind: "pending", clientMutationId: "mutation-1" },
		menu: ["edit", "cancel"],
		localQueued: true,
	});
});

function mount(surface: "rows" | "sheet", list: Ghost[], disabled = false) {
	const calls: Array<[string, GhostAction | undefined]> = [];
	const action = vi.fn(async (ghost: Ghost, action: GhostAction) => {
		calls.push([ghost.key, action]);
		return null;
	});
	platform.goBack.mockImplementation(() => calls.push(["goBack", undefined]));
	const sheetHost = (next: Ghost[], busy: boolean) => ({
		ghosts: next,
		disabled: busy,
		act: action,
		showOnSession: vi.fn(),
	});
	const rows = (next: Ghost[], busy: boolean) => (
		<GhostRowContext.Provider
			value={{
				disabled: busy,
				canEdit: false,
				editHint: null,
				canEditParked: false,
				parkedEditHint: null,
				onAction: action,
				onMore: vi.fn(),
			}}
		>
			{withGhostRows([], next)
				.filter(isGhostRow)
				.map((row) => (
					<GhostRowView key={row.id} row={row} />
				))}
		</GhostRowContext.Provider>
	);
	if (surface === "sheet") act(() => queueHosts.provide(sheetKey("hub-1", "ref-1"), owner, sheetHost(list, disabled)));
	const tree = render(
		surface === "rows" ? (
			rows(list, disabled)
		) : (
			<QueueSheet
				route={{ key: "queue", name: "QueueSheet", params: { hubId: "hub-1", ref: "ref-1" } } as never}
				navigation={platform as never}
			/>
		),
	);
	return {
		tree,
		action,
		calls,
		update(next: Ghost[], busy = false) {
			act(() => {
				if (surface === "rows") tree.update(rows(next, busy));
				else queueHosts.provide(sheetKey("hub-1", "ref-1"), owner, sheetHost(next, busy));
			});
		},
	};
}
async function choose(tree: ReactTestRenderer, ghost: Ghost, index: number) {
	platform.menu.mockClear();
	act(() => pressable(tree, `${ghost.text}. ${ghost.caption}`)?.props.onPress());
	expect(platform.menu).toHaveBeenCalledOnce();
	const [, pick] = platform.menu.mock.calls[0];
	await act(async () => pick(index));
}

const image = { marker: 7, mediaType: "image/png", name: "source.png", data: "AP8H" };
const compositions = [
	{
		text: "full /skill [image 7] /command",
		attachments: [image],
		skillNames: ["skill"],
		commandNames: ["command"],
		mentions: [{ kind: "skill" as const, name: "skill", offset: 5 }],
	},
	{ text: "", attachments: [image], skillNames: [], commandNames: [], mentions: [] },
	{ text: "", attachments: [], skillNames: ["skill"], commandNames: ["command"], mentions: [] },
];
for (const surface of ["rows", "sheet"] as const) {
	test(`${surface} removes the selected identity among equal-text local submissions`, async () => {
		const runtime = fixture();
		await submit(runtime);
		await submit(runtime);
		const { pending, local } = await snapshot(runtime);
		const list = ghosts(null, pending, null, [], false, local);
		const view = mount(surface, list);
		const second = view.tree.root.findAllByType(GhostBubble)[1];
		const bubble = second.findAll(
			(node) =>
				String(node.type) === "Pressable" && node.props.accessibilityLabel === `${list[1].text}. ${list[1].caption}`,
		)[0];
		act(() => bubble.props.onPress());
		const [, pick] = platform.menu.mock.calls.at(-1)!;
		await act(async () => pick(1));
		expect(view.action.mock.calls).toEqual([[list[1], "cancel"]]);
		expect(view.calls).toEqual([["pending:mutation-2", "cancel"]]);
		expect((await runtime.read(targetRef)).outbox.map((record) => record.clientMutationId)).toEqual([
			"mutation-1",
			"mutation-2",
		]);
	});
	for (const held of [false, true]) {
		test.each(compositions)(
			`${surface} ${held ? "held" : "waiting"} routes complete, image and selection-only Edit by identity: %j`,
			async (composer) => {
				const runtime = fixture();
				await submit(runtime, {
					composer,
					input: buildComposerInput(
						composer.text,
						composer.attachments,
						composer.skillNames,
						composer.commandNames,
						composer.mentions,
					),
				});
				if (held)
					await runtime.storage.enqueueInterruptAndCancel({
						targetRef,
						method: "turn/interrupt",
						payload: { ref: "ref-1" },
						attachments: [],
						optimisticDisplay: null,
					});
				const { pending, local } = await snapshot(runtime);
				const list = ghosts(null, pending, null, [], false, local);
				expect(list).toHaveLength(1);
				expect(list[0].menu).toEqual(["edit", "cancel"]);
				if (held) expect(list[0].buttons).toEqual(["sendNow", "cancel"]);
				const view = mount(surface, list);
				await choose(view.tree, list[0], 0);
				expect(view.action.mock.calls).toEqual([[list[0], "edit"]]);
				expect(view.calls).toEqual(
					surface === "sheet"
						? [
								["goBack", undefined],
								["pending:mutation-1", "edit"],
							]
						: [["pending:mutation-1", "edit"]],
				);
				expect((await runtime.read(targetRef)).outbox[0]).toEqual(local.records[0]); // presentation does not claim ownership
			},
		);
	}
	test.each([
		["unavailable", { ...ready, document: null }],
		["loading", { ...ready, document: { ...ready.document, loaded: false } }],
		["submitting", { ...ready, document: { ...ready.document, submitting: true } }],
		["unsaved", { ...ready, document: { ...ready.document, error: "save-failure" } }],
		["encoding", { ...ready, encodingAttachments: true }],
	] as const)(`${surface} blocks local merge when %s but retains Remove`, async (_condition, readiness) => {
		const runtime = fixture();
		await submit(runtime);
		const { pending, local } = await snapshot(runtime);
		const list = ghosts(null, pending, null, [], false, { ...local, readiness });
		expect(list[0].menu).toEqual(["cancel"]);
		const view = mount(surface, list);
		await choose(view.tree, list[0], 0);
		expect(view.action.mock.calls).toEqual([[list[0], "cancel"]]);
	});
}

const daemon: GhostSource = {
	status: { type: "active" },
	capabilities: {
		send: true,
		steer: true,
		interrupt: true,
		compact: true,
		clear: true,
		forkFromTurn: true,
		shutdown: true,
		changeModel: true,
		changeVisionModel: true,
		queue: true,
		goal: true,
		sharedNotes: true,
		rename: true,
	},
	queue: { revision: 3, depth: 2, ids: ["daemon-1", "daemon-2"], texts: ["a", "b"], preview: ["a", "b"] },
};

test("mixed daemon, waiting and held queues share the three-row limit without hiding uncertain rows", async () => {
	const runtime = fixture();
	await submit(runtime);
	await submit(runtime, { kind: "interrupt", input: [] });
	await submit(runtime);
	await submit(runtime);
	await submit(runtime);
	await runtime.storage.markAttempted("mutation-5");
	await runtime.storage.markUnknown("mutation-5", "blockedUnknown");
	const { pending, local } = await snapshot(runtime);
	const list = ghosts(daemon, pending, null, [], true, local);
	expect(list.map((row) => row.key)).toEqual([
		"queue:daemon-1",
		"queue:daemon-2",
		"pending:mutation-1",
		"pending:mutation-3",
		"pending:mutation-4",
		"pending:mutation-5",
	]);
	expect(shownGhosts(list)).toMatchObject({ moreQueued: 2 });
	expect(shownGhosts(list).shown.map((row) => row.key)).toEqual([
		"queue:daemon-1",
		"queue:daemon-2",
		"pending:mutation-1",
		"pending:mutation-5",
	]);
	const sheetQueue = list.filter(isQueuedGhost);
	expect(sheetQueue.map((row) => row.key)).toEqual([
		"queue:daemon-1",
		"queue:daemon-2",
		"pending:mutation-1",
		"pending:mutation-3",
		"pending:mutation-4",
	]);
	const rows = mount("rows", list);
	expect(rows.tree.root.findAllByType(GhostRowView).map((node) => node.props.row.kind)).toEqual([
		"ghost",
		"ghost",
		"ghost",
		"ghost",
		"moreQueued",
	]);
	expect(rows.tree.root.findAllByType(GhostRowView).at(-1)?.props.row.count).toBe(2);
	const sheet = mount("sheet", sheetQueue);
	expect(
		sheet.tree.root.findAll((node) => String(node.type) === "View" && node.props.testID?.startsWith("ghost:")).length,
	).toBe(5);
});

test("a safely waiting send matched by the draft keeps its local identity and is shown only once", async () => {
	const runtime = fixture();
	await submit(runtime);
	const { pending, local } = await snapshot(runtime);
	const list = ghosts(null, pending, { text: "same", sentText: "same" }, [], false, local);
	expect(list).toHaveLength(1);
	expect(list[0]).toMatchObject({
		origin: { kind: "pending", clientMutationId: "mutation-1" },
		localQueued: true,
		menu: ["edit", "cancel"],
	});
});

test("a matched local send retains the draft attachment presentation", async () => {
	const runtime = fixture();
	await submit(runtime, { input: buildComposerInput("same", [image]) });
	const { pending, local } = await snapshot(runtime);
	const list = ghosts(null, pending, { text: "same", sentText: "same" }, [], false, local);
	const tree = render(
		<GhostRowContext.Provider
			value={{
				disabled: false,
				canEdit: false,
				editHint: null,
				canEditParked: false,
				parkedEditHint: null,
				onAction: vi.fn(),
				onMore: vi.fn(),
				draftAttachments: <View testID="draft-image-preview" />,
			}}
		>
			{withGhostRows([], list)
				.filter(isGhostRow)
				.map((row) => (
					<GhostRowView key={row.id} row={row} />
				))}
		</GhostRowContext.Provider>,
	);
	expect(
		tree.root.findAll((node) => String(node.type) === "View" && node.props.testID === "draft-image-preview"),
	).toHaveLength(1);
});

for (const surface of ["rows", "sheet"] as const) {
	test.each(["busy", "encoding", "attempted"] as const)(
		`${surface} ignores a menu selection after live state becomes %s`,
		async (change) => {
			const runtime = fixture();
			await submit(runtime);
			const { pending, local } = await snapshot(runtime);
			const list = ghosts(null, pending, null, [], false, local);
			const view = mount(surface, list);
			act(() => pressable(view.tree, `${list[0].text}. ${list[0].caption}`)?.props.onPress());
			const [, pick] = platform.menu.mock.calls.at(-1)!;
			if (change === "busy") view.update(list, true);
			else if (change === "encoding")
				view.update(
					ghosts(null, pending, null, [], false, { ...local, readiness: { ...ready, encodingAttachments: true } }),
				);
			else {
				await runtime.storage.markAttempted("mutation-1");
				const next = await snapshot(runtime);
				view.update(ghosts(null, next.pending, null, [], true, next.local));
			}
			await act(async () => pick(0));
			expect(view.action).not.toHaveBeenCalled();
			expect(platform.goBack).not.toHaveBeenCalled();
		},
	);
	test.each(["attempted", "accepted", "uncertain"] as const)(
		`${surface} keeps %s delivery out of local editing`,
		async (state) => {
			const runtime = fixture();
			await submit(runtime);
			await runtime.storage.markAttempted("mutation-1");
			if (state === "accepted") await runtime.storage.settleReceipt("mutation-1", "pending");
			if (state === "uncertain") await runtime.storage.markUnknown("mutation-1", "blockedUnknown");
			const { pending, local } = await snapshot(runtime);
			const list = ghosts(null, pending, null, [], true, local);
			expect(list).toHaveLength(1);
			expect(list[0].localQueued).not.toBe(true);
			expect(list[0].menu).toEqual([]);
			expect(list[0].buttons).toEqual(state === "uncertain" ? ["check", "discard"] : []);
			const view = mount(surface, list);
			expect(view.tree.root.findAllByType("ReanimatedSwipeable" as never)).toHaveLength(0);
			if (state === "uncertain") {
				await act(async () => pressable(view.tree, "Check")?.props.onPress());
				expect(view.action.mock.calls).toEqual([[list[0], "check"]]);
			}
		},
	);
	test(`${surface} denies missing or wrong scoped durable proof`, async () => {
		const runtime = fixture();
		await submit(runtime);
		const { pending, local } = await snapshot(runtime);
		for (const input of [
			undefined,
			{ ...local, records: [] },
			{ ...local, targetRef: "other-target" },
			{ ...local, threadId: "other-thread" },
			{ ...local, instanceId: "other-instance" },
		]) {
			const list = ghosts(null, pending, null, [], false, input);
			expect(list[0].localQueued).not.toBe(true);
			expect(list[0].menu).toEqual([]);
			const view = mount(surface, list);
			act(() => pressable(view.tree, `${list[0].text}. ${list[0].caption}`)?.props.onPress());
			expect(view.action).not.toHaveBeenCalled();
		}
	});
	test(`${surface} never grants local controls to an accepted projection even with an older durable snapshot`, async () => {
		const runtime = fixture();
		await submit(runtime);
		const { local } = await snapshot(runtime);
		await runtime.storage.markAttempted("mutation-1");
		await runtime.storage.settleReceipt("mutation-1", "pending");
		const { pending } = await snapshot(runtime);
		expect(pending[0].state).toBe("accepted");
		const list = ghosts(null, pending, null, [], true, local);
		expect(list[0].menu).toEqual([]);
		expect(list[0].localQueued).not.toBe(true);
		const view = mount(surface, list);
		expect(view.tree.root.findAllByType("ReanimatedSwipeable" as never)).toHaveLength(0);
	});
	test.each([
		{
			kind: "promote" as const,
			input: [],
			queueEntry: { index: 0, id: "daemon-1" },
			display: [{ type: "text" as const, text: "preview-not-composer" }],
		},
		{
			kind: "steer" as const,
			input: [],
			expectedQueueRevision: 3,
			display: [{ type: "text" as const, text: "preview-not-composer" }],
		},
	])(`${surface} offers Remove only for a queue control: %j`, async (request) => {
		const runtime = fixture();
		await submit(runtime, request);
		await submit(runtime, { kind: "interrupt", input: [] });
		const { pending, local } = await snapshot(runtime);
		const list = ghosts(null, pending, null, [], false, local);
		expect(list).toHaveLength(1);
		expect(list[0]).toMatchObject({ localQueued: true, menu: ["cancel"], buttons: ["sendNow", "cancel"] });
		const view = mount(surface, list);
		await choose(view.tree, list[0], 0);
		expect(view.action.mock.calls).toEqual([[list[0], "cancel"]]);
	});
	test(`${surface} routes a content-bearing drain to its own identity rather than its queue preview`, async () => {
		const runtime = fixture();
		await submit(runtime, {
			kind: "steer",
			expectedQueueRevision: 3,
			input: [{ type: "text", text: "own-input" }],
			display: [{ type: "text", text: "queue-preview" }],
		});
		await submit(runtime, { kind: "interrupt", input: [] });
		const { pending, local } = await snapshot(runtime);
		const list = ghosts({ ...daemon, queue: { ...daemon.queue!, revision: 4 } }, pending, null, [], false, local);
		const drain = list.find((row) => row.origin.kind === "pending")!;
		expect(drain.buttons).toEqual(["cancel"]); // stale queue still cannot be drained
		expect(drain.menu).toEqual(["edit", "cancel"]);
		const view = mount(surface, [drain]);
		await choose(view.tree, drain, 0);
		expect(view.action.mock.calls).toEqual([[drain, "edit"]]);
		expect(local.records[0].payload.input).toEqual([{ type: "text", text: "own-input" }]);
	});
	test(`${surface} follows live readiness and admission changes and disables every removal gesture`, async () => {
		const runtime = fixture();
		await submit(runtime);
		await submit(runtime, { kind: "interrupt", input: [] });
		const { pending, local } = await snapshot(runtime);
		const list = ghosts(null, pending, null, [], false, local);
		const view = mount(surface, list, true);
		await act(async () => pressable(view.tree, "Remove")?.props.onPress());
		act(() => pressable(view.tree, `${list[0].text}. ${list[0].caption}`)?.props.onPress());
		swipeRowFully(view.tree.root.findByType("ReanimatedSwipeable" as never), "left", { pageX: 200 });
		expect(view.action).not.toHaveBeenCalled();
		expect(platform.menu).not.toHaveBeenCalled();
		view.update(list);
		await act(async () => pressable(view.tree, "Remove")?.props.onPress());
		expect(view.action.mock.calls).toEqual([[list[0], "cancel"]]);
		view.action.mockClear();
		swipeRowFully(view.tree.root.findByType("ReanimatedSwipeable" as never), "left", { pageX: 200 });
		expect(view.action.mock.calls).toEqual([[list[0], "cancel"]]);
		view.action.mockClear();
		const blocked = ghosts(null, pending, null, [], false, {
			...local,
			readiness: { ...ready, encodingAttachments: true },
		});
		view.update(blocked);
		await choose(view.tree, blocked[0], 0);
		expect(view.action.mock.calls).toEqual([[blocked[0], "cancel"]]);
		await runtime.storage.releaseCanceled("mutation-1", targetRef, {
			stopEpoch: await runtime.storage.readStopEpoch(targetRef),
		});
		await runtime.storage.markAttempted("mutation-1");
		const next = await snapshot(runtime);
		view.update(ghosts(null, next.pending, null, [], true, next.local));
		expect(view.tree.root.findAllByType("ReanimatedSwipeable" as never)).toHaveLength(0);
	});
}

function draftFixture() {
	const directory = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "local-ghost-draft-"));
	const opened = openSqliteSyncDouble(join(directory, "drafts.sqlite"));
	const repository = new DraftRepository(opened.port);
	const document = new DraftDocument(() => repository, { hubId: "hub-1", sessionRef: "ref-1" });
	cleanups.push(async () => {
		opened.database.close();
		rmSync(directory, { recursive: true });
	});
	return { document, database: opened.database };
}
for (const surface of ["rows", "sheet"] as const) {
	test(`${surface} uses a real occupied draft snapshot without overwriting it and blocks a failed save`, async () => {
		const runtime = fixture();
		const { document, database } = draftFixture();
		document.edit("current-work");
		expect(document.recoveredRestoreBlock()).toBe("occupied");
		await submit(runtime);
		const { pending, local } = await snapshot(runtime);
		const list = ghosts(null, pending, null, [], false, {
			...local,
			readiness: { document: document.getSnapshot(), encodingAttachments: false },
		});
		const view = mount(surface, list);
		await choose(view.tree, list[0], 0);
		expect(view.action.mock.calls).toEqual([[list[0], "edit"]]);
		expect(document.getSnapshot().record.draft).toBe("current-work");
		database.exec("PRAGMA query_only = ON");
		document.edit("unsaved-work");
		expect(document.getSnapshot().error).not.toBeNull();
		const blocked = ghosts(null, pending, null, [], false, {
			...local,
			readiness: { document: document.getSnapshot(), encodingAttachments: false },
		});
		expect(blocked[0].menu).toEqual(["cancel"]);
		database.exec("PRAGMA query_only = OFF");
	});
}

test("ordinary rejected recovery keeps its occupied composer guard and image exclusion", async () => {
	const runtime = fixture();
	await submit(runtime);
	await submit(runtime, { input: buildComposerInput("with-image", [image]) });
	await runtime.storage.transferToRecovery("mutation-1", "rejected");
	await runtime.storage.transferToRecovery("mutation-2", "rejected");
	const recovery = projectNativeMutationRecovery(targetRef, await runtime.read(targetRef));
	const list = ghosts(null, [], null, recovery, false);
	expect(list.map((row) => row.buttons)).toEqual([["edit", "discard"], ["discard"]]);
	expect(list.filter(isQueuedGhost)).toEqual([]);
	const view = mount("rows", list);
	await act(async () => pressable(view.tree, "Edit")?.props.onPress());
	expect(view.action).not.toHaveBeenCalled();
	expect(pressable(view.tree, "Edit")?.props.disabled).toBe(true);
});

test("real Stop and Note records never become local message ghosts", async () => {
	const runtime = fixture();
	await submit(runtime, { kind: "interrupt", input: [] });
	await runtime.storage.enqueueIntent({
		targetRef,
		threadId: scope.threadId,
		instanceId: scope.instanceId,
		method: "notes/human/set",
		payload: { ref: "ref-1", expectedInstanceId: scope.instanceId, note: "note-input" },
		attachments: [],
		optimisticDisplay: null,
	});
	const { pending, local } = await snapshot(runtime);
	expect(local.records).toHaveLength(2);
	expect(ghosts(null, pending, null, [], false, local)).toEqual([]);
});
