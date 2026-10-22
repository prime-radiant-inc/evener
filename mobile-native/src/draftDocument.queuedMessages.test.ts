import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { buildComposerInput } from "@evener/appwire-client";
import type { MutationRecoveryRecord } from "@evener/appwire-client/state/mutation";
import { DraftDocument } from "./draftDocument";
import { DraftRepository } from "./draftRepository";
import { MutationOutboxSQLite } from "./mutationOutboxStorage";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

const cryptoTestState = vi.hoisted(() => ({ nextToken: 0 }));
vi.mock("expo-crypto", () => ({
	randomUUID: () => `draft-capture-${++cryptoTestState.nextToken}`,
	getRandomValues: (array: Uint8Array) => array,
}));

let directory: string;
let database: SqliteDoubleDatabase;
let repository: DraftRepository;
let document: DraftDocument;
let imageId = 0;
const destination = { hubId: "hub", sessionRef: "session" };
const targetRef = JSON.stringify(["hub", "session"]);
function open() {
	const opened = openSqliteSyncDouble(join(directory, "drafts.sqlite"));
	database = opened.database;
	repository = new DraftRepository(opened.port);
	document = new DraftDocument(
		() => repository,
		destination,
		() => `recovered-${++imageId}`,
	);
}
function reopen() {
	database.close();
	open();
}
beforeEach(() => {
	cryptoTestState.nextToken = 0;
	directory = mkdtempSync(join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "draft-ownership-"));
	open();
});
afterEach(() => {
	database.close();
	rmSync(directory, { recursive: true, force: true });
});
function recovery(id = "source-a", overrides: Partial<MutationRecoveryRecord> = {}): MutationRecoveryRecord {
	return {
		version: 1,
		clientMutationId: id,
		targetRef,
		method: "turn/start",
		payload: {
			input: [
				{ type: "text", text: "encoded" },
				{ type: "image", mediaType: "image/png", data: "AP8H" },
				{ type: "skill", name: "recovered-skill" },
				{ type: "command", name: "recovered-command" },
			],
		},
		attachments: [{ presentationId: "original", marker: 9, name: "photo.png", mediaType: "image/png" }],
		composerText: "[image 9] tail",
		composerMentions: [{ kind: "skill", name: "recovered-skill", offset: 10 }],
		optimisticDisplay: {},
		intentSequence: 1,
		createdAt: 1,
		state: "canceled",
		attempted: false,
		recoveryKind: "editing",
		...overrides,
	};
}

it("appends complete composition beside current work and deduplicates exact IDs across file reopen", () => {
	const current = { id: "current", marker: 1, mediaType: "image/png", data: "AQID" };
	repository.write(
		destination,
		{
			draft: "[image 1] here",
			unconfirmed: null,
			images: [current],
			composer: {
				skillNames: ["current-skill"],
				commandNames: ["current-command"],
				mentions: [{ kind: "command", name: "current-command", offset: 10 }],
			},
		},
		[current],
	);
	reopen();
	expect(document.appendEditedRecovery(recovery())).toBe("restored");
	expect(document.appendEditedRecovery(recovery())).toBe("already-restored");
	reopen();
	expect(document.appendEditedRecovery(recovery())).toBe("already-restored");
	expect(document.appendEditedRecovery(recovery("source-b"))).toBe("restored");
	reopen();
	expect(document.getSnapshot().record).toMatchObject({
		draft: "[image 1] here\n\n[image 2] tail\n\n[image 3] tail",
		composer: {
			recoveryIds: ["source-a", "source-b"],
			skillNames: ["current-skill", "recovered-skill"],
			commandNames: ["current-command", "recovered-command"],
			mentions: [
				{ kind: "command", name: "current-command", offset: 10 },
				{ kind: "skill", name: "recovered-skill", offset: 26 },
				{ kind: "skill", name: "recovered-skill", offset: 42 },
			],
		},
	});
	expect(document.imagePreviews().map(({ marker, data }) => [marker, data])).toEqual([
		[1, "AQID"],
		[2, "AP8H"],
		[3, "AP8H"],
	]);
});

it.each([
	{ label: "insertion before mentions", text: "please hello /inspect then /inspect", offsets: [13, 27] },
	{ label: "non-BMP insertion before mentions", text: "😀 please hello /inspect then /inspect", offsets: [16, 30] },
	{ label: "unchanged text", text: "hello /inspect then /inspect", offsets: [6, 20] },
	{ label: "deletion before mentions", text: "/inspect then /inspect", offsets: [0, 14] },
	{ label: "replacement of a label", text: "hello /changed then /inspect", offsets: [20] },
	{ label: "deletion of a label", text: "hello  then /inspect", offsets: [12] },
	{ label: "new matching prose", text: "hello /inspect then /inspect /inspect", offsets: [6, 20] },
])("ordinary typing preserves anchors and ownership: $label", async ({ text, offsets }) => {
	repository.write(destination, { draft: "", unconfirmed: null, recoveryResendAcks: ["pending-ack"] });
	reopen();
	const original = "hello /inspect then /inspect";
	const mentions = [
		{ kind: "skill" as const, name: "inspect", offset: 6 },
		{ kind: "command" as const, name: "inspect", offset: 20 },
	];
	expect(
		document.appendEditedRecovery(
			recovery("exact-source", {
				composerText: original,
				composerMentions: mentions,
				attachments: [],
				payload: {
					input: [
						{ type: "text", text: original, mentions },
						{ type: "skill", name: "inspect" },
						{ type: "command", name: "inspect" },
					],
				},
			}),
		),
	).toBe("restored");
	document.edit(text);
	reopen();
	const expectedMentions = offsets.map((offset, index) => ({
		kind: offsets.length === 1 || index === 1 ? "command" : "skill",
		name: "inspect",
		offset,
	}));
	expect(document.getSnapshot().record).toMatchObject({
		draft: text,
		composer: {
			skillNames: offsets.length === 1 ? [] : ["inspect"],
			commandNames: ["inspect"],
			mentions: expectedMentions,
			recoveryIds: ["exact-source"],
		},
		recoveryResendAcks: ["pending-ack"],
	});
	let submitted = false;
	await document.submit(async (draft, images, context) => {
		submitted = true;
		expect(context.recoverySourceIds).toEqual(["exact-source"]);
		expect(
			buildComposerInput(
				draft,
				images,
				context.composer?.skillNames,
				context.composer?.commandNames,
				context.composer?.mentions,
			),
		).toEqual([
			{ type: "text", text, mentions: expectedMentions },
			...(offsets.length === 1 ? [] : [{ type: "skill" as const, name: "inspect" }]),
			{ type: "command", name: "inspect" },
		]);
		return false;
	});
	expect(submitted).toBe(true);
	expect(repository.read(destination).recoveryResendAcks).toEqual(["pending-ack"]);
});

it("deselects canonical names when their last recorded label is edited", () => {
	const before = "😀 /same and /same plus prose /same";
	const mentions = [
		{ kind: "skill" as const, name: "same", offset: 3 },
		{ kind: "command" as const, name: "same", offset: 13 },
	];
	expect(mentions.map(({ offset, name }) => before.slice(offset, offset + name.length + 1))).toEqual([
		"/same",
		"/same",
	]);
	const source = recovery("labels", {
		composerText: before,
		composerMentions: mentions,
		attachments: [],
		payload: {
			input: [
				{ type: "text", text: before, mentions },
				{ type: "skill", name: "same" },
				{ type: "command", name: "same" },
				{ type: "skill", name: "label-free-skill" },
				{ type: "command", name: "label-free-command" },
			],
		},
	});
	expect(document.appendEditedRecovery(source)).toBe("restored");
	document.edit("😀 renamed and  plus prose /same");
	expect(document.getSnapshot().record).toMatchObject({
		draft: "😀 renamed and  plus prose /same",
		composer: {
			skillNames: ["label-free-skill"],
			commandNames: ["label-free-command"],
			mentions: [],
			recoveryIds: ["labels"],
		},
	});
});

it("drops a selected skill when typing a token character before its label and submits no stale selection", async () => {
	const before = "/skill";
	const mention = { kind: "skill" as const, name: "skill", offset: 0 };
	const source = recovery("leading-boundary", {
		composerText: before,
		composerMentions: [mention],
		attachments: [],
		payload: {
			input: [
				{ type: "text", text: before, mentions: [mention] },
				{ type: "skill", name: "skill" },
			],
		},
	});
	expect(document.appendEditedRecovery(source)).toBe("restored");
	document.edit("x/skill");
	expect(document.getSnapshot().record.composer).toMatchObject({
		skillNames: [],
		mentions: [],
		recoveryIds: ["leading-boundary"],
	});
	await document.submit(async (draft, images, context) => {
		expect(
			buildComposerInput(
				draft,
				images,
				context.composer?.skillNames,
				context.composer?.commandNames,
				context.composer?.mentions,
			),
		).toEqual([{ type: "text", text: "x/skill" }]);
		return false;
	});
});

it("keeps a canonical selection while another recorded label of the same kind and name survives", () => {
	const before = "😀 /same then /same";
	const mentions = [
		{ kind: "skill" as const, name: "same", offset: 3 },
		{ kind: "skill" as const, name: "same", offset: 14 },
	];
	repository.write(destination, {
		draft: before,
		unconfirmed: null,
		composer: { skillNames: ["same", "independent"], mentions },
	});
	reopen();
	document.edit("😀 /other then /same");
	expect(document.getSnapshot().record.composer).toMatchObject({
		skillNames: ["same", "independent"],
		mentions: [{ kind: "skill", name: "same", offset: 15 }],
	});
});

it("keeps legacy prose unbound through collisions and a later merge", () => {
	document.edit("[image 1] existing");
	document.addImage({ id: "current", marker: 9, mediaType: "image/png", data: "AQID" });
	const legacy = recovery("legacy", {
		composerText: undefined,
		composerMentions: undefined,
		payload: {
			input: [
				{ type: "text", text: "literal [image 9] gap [image 3]" },
				{ type: "image", data: "AP8H", mediaType: "image/png" },
			],
		},
	});
	expect(document.appendEditedRecovery(legacy)).toBe("restored");
	expect(document.getSnapshot().record.draft).toBe("[image 1] existing[image 2]\n\nliteral [image 9] gap [image 3]");
	expect(document.imagePreviews().map((i) => i.marker)).toEqual([2, 4]);
	reopen();
	expect(document.appendEditedRecovery(recovery())).toBe("restored");
	expect(document.getSnapshot().record.draft).toBe(
		"[image 1] existing[image 2]\n\nliteral [image 9] gap [image 3]\n\n[image 5] tail",
	);
	expect(document.imagePreviews().map((i) => i.data)).toEqual(["AQID", "AP8H", "AP8H"]);
});

it("rolls back failed append without consuming the original recovery or prior draft", () => {
	document.edit("prior");
	const source = recovery();
	const original = structuredClone(source);
	database.exec("PRAGMA query_only = ON");
	expect(document.appendEditedRecovery(source)).toBe("blocked");
	expect(repository.read(destination)).toEqual({ draft: "prior", unconfirmed: null });
	expect(source).toEqual(original);
	database.exec("PRAGMA query_only = OFF");
	document.retry();
	expect(document.appendEditedRecovery(source)).toBe("restored");
	reopen();
	expect(document.imagePreviews()[0]?.data).toBe("AP8H");
	expect(document.getSnapshot().record.composer?.recoveryIds).toEqual(["source-a"]);
});

it("refuses recovery for another target or ordinary rejected input", () => {
	expect(document.appendEditedRecovery(recovery("wrong", { targetRef: JSON.stringify(["other", "session"]) }))).toBe(
		"blocked",
	);
	expect(document.appendEditedRecovery(recovery("rejected", { recoveryKind: "rejected" }))).toBe("blocked");
	expect(repository.read(destination)).toEqual({ draft: "", unconfirmed: null });
});

it.each(["image", "skill", "command"] as const)(
	"captures %s-only edited ownership before operation and waits for a matching receipt",
	async (kind) => {
		const source = recovery("only", {
			composerText: "",
			composerMentions: undefined,
			payload: {
				input: [
					kind === "image"
						? { type: "image", mediaType: "image/png", data: "AP8H" }
						: { type: kind, name: "selection" },
				],
			},
			attachments: kind === "image" ? recovery().attachments : [],
		});
		expect(document.appendEditedRecovery(source)).toBe("restored");
		let calls = 0;
		await document.submit(async (text, images, context) => {
			calls++;
			expect(repository.read(destination)).toMatchObject({
				draft: "",
				unconfirmed: "",
				unconfirmedComposer: { recoveryIds: ["only"] },
			});
			expect(context.recoverySourceIds).toEqual(["only"]);
			expect(context.composer).toEqual({
				text: "",
				attachments: images,
				skillNames: kind === "skill" ? ["selection"] : [],
				commandNames: kind === "command" ? ["selection"] : [],
			});
			expect(text).toBe("");
			expect(images.map((i) => i.data)).toEqual(kind === "image" ? ["AP8H"] : []);
			document.edit("newer");
			document.addImage({ id: "newer-image", marker: 8, mediaType: "image/png", data: "AQID" });
			return true;
		});
		expect(calls).toBe(1);
		reopen();
		expect(document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toEqual(["only"]);
		expect(document.appendEditedRecovery(source)).toBe("already-restored");
		expect(document.settleEditedResend({ targetRef, sourceIds: ["other"], clientMutationId: "fresh" })).toBe(false);
		expect(document.settleEditedResend({ targetRef: "wrong", sourceIds: ["only"], clientMutationId: "fresh" })).toBe(
			false,
		);
		expect(document.settleEditedResend({ targetRef, sourceIds: ["only"], clientMutationId: "fresh" })).toBe(true);
		reopen();
		expect(document.getSnapshot().record).toMatchObject({
			draft: "newer[image 8]",
			unconfirmed: null,
			recoveryResendAcks: ["fresh"],
		});
		expect(document.getSnapshot().record.unconfirmedComposer).toBeUndefined();
		expect(document.imagePreviews().map((i) => i.data)).toEqual(["AQID"]);
		expect(document.finishRecoveryResendAck("unrelated")).toBe(true);
		expect(repository.read(destination).recoveryResendAcks).toEqual(["fresh"]);
		expect(document.finishRecoveryResendAck("fresh")).toBe(true);
		expect(document.finishRecoveryResendAck("fresh")).toBe(true);
		reopen();
		expect(document.getSnapshot().record.recoveryResendAcks).toBeUndefined();
	},
);

it("captures full original context and exact source set, preserving newer recovered metadata at settlement", async () => {
	document.appendEditedRecovery(recovery("a"));
	document.appendEditedRecovery(recovery("b"));
	await document.submit(async (text, images, context) => {
		expect(context).toEqual({
			draftCaptureToken: "draft-capture-1",
			composer: {
				text: "[image 1] tail\n\n[image 2] tail",
				attachments: images,
				skillNames: ["recovered-skill"],
				commandNames: ["recovered-command"],
				mentions: [
					{ kind: "skill", name: "recovered-skill", offset: 10 },
					{ kind: "skill", name: "recovered-skill", offset: 26 },
				],
			},
			recoverySourceIds: ["a", "b"],
		});
		expect(repository.read(destination).unconfirmedCaptureToken).toBe(context.draftCaptureToken);
		expect(text).toBe(context.composer?.text);
		expect(images.map((i) => [i.marker, i.data])).toEqual([
			[1, "AP8H"],
			[2, "AP8H"],
		]);
		return true;
	});
	expect(document.appendEditedRecovery(recovery("c"))).toBe("restored");
	const newer = repository.read(destination);
	for (const sourceIds of [["a"], ["a", "b", "c"], ["a", "a"]]) {
		expect(document.settleEditedResend({ targetRef, sourceIds, clientMutationId: "fresh" })).toBe(false);
	}
	expect(document.settleEditedResend({ targetRef, sourceIds: ["b", "a"], clientMutationId: "fresh" })).toBe(true);
	expect(repository.read(destination)).toMatchObject({
		draft: newer.draft,
		composer: newer.composer,
		images: newer.images,
		recoveryResendAcks: ["fresh"],
	});
});

it("does not call the operation on failed capture and retries receipt and acknowledgment writes durably", async () => {
	document.appendEditedRecovery(recovery());
	database.exec("PRAGMA query_only = ON");
	let calls = 0;
	await expect(
		document.submit(async () => {
			calls++;
			return true;
		}),
	).rejects.toBeDefined();
	expect(calls).toBe(0);
	database.exec("PRAGMA query_only = OFF");
	document.retry();
	await document.submit(async () => true);
	const captured = repository.read(destination);
	const receipt = { targetRef, sourceIds: ["source-a"], clientMutationId: "fresh" };
	database.exec("PRAGMA query_only = ON");
	expect(document.settleEditedResend(receipt)).toBe(false);
	expect(document.getSnapshot().record).toEqual(captured);
	expect(repository.read(destination)).toEqual(captured);
	database.exec("PRAGMA query_only = OFF");
	document.retry();
	expect(document.settleEditedResend(receipt)).toBe(true);
	reopen();
	database.exec("PRAGMA query_only = ON");
	expect(document.finishRecoveryResendAck("fresh")).toBe(false);
	expect(document.getSnapshot().record.recoveryResendAcks).toEqual(["fresh"]);
	database.exec("PRAGMA query_only = OFF");
	document.retry();
	expect(document.finishRecoveryResendAck("fresh")).toBe(true);
	reopen();
	expect(document.getSnapshot().record.unconfirmed).toBeNull();
	expect(document.getSnapshot().record.recoveryResendAcks).toBeUndefined();
});

it("restores captured edits only with every retained source, merging newer work without using original source text", async () => {
	const a = recovery("a");
	const b = recovery("b");
	document.appendEditedRecovery(a);
	document.appendEditedRecovery(b);
	document.edit("revision [image 1] [image 2]");
	await document.submit(async () => false);
	document.edit("new [image 7]");
	document.addImage({ id: "new", marker: 4, data: "AQID", mediaType: "image/png" });
	const before = repository.read(destination);
	expect(document.restoreEditedRecovery([])).toBe(false);
	expect(document.restoreEditedRecovery([a])).toBe(false);
	expect(document.restoreEditedRecovery([a, a])).toBe(false);
	expect(document.restoreEditedRecovery([a, { ...b, targetRef: "wrong" }])).toBe(false);
	expect(document.restoreEditedRecovery([a, { ...b, recoveryKind: "rejected" }])).toBe(false);
	expect(repository.read(destination)).toEqual(before);
	database.exec("PRAGMA query_only = ON");
	expect(document.restoreEditedRecovery([a, b])).toBe(false);
	expect(repository.read(destination)).toEqual(before);
	database.exec("PRAGMA query_only = OFF");
	document.retry();
	expect(document.restoreEditedRecovery([b, a])).toBe(true);
	reopen();
	expect(document.getSnapshot().record).toMatchObject({
		draft: "new [image 7][image 4]\n\nrevision [image 1] [image 2]",
		unconfirmed: null,
		composer: { recoveryIds: ["a", "b"] },
	});
	expect(document.imagePreviews().map((i) => i.data)).toEqual(["AQID", "AP8H", "AP8H"]);
	expect(document.restoreEditedRecovery([a, b])).toBe(false);
	expect(document.appendEditedRecovery(a)).toBe("already-restored");
	let calls = 0;
	await document.submit(async (_text, _images, context) => {
		calls++;
		expect(context.recoverySourceIds).toEqual(["a", "b"]);
		return true;
	});
	expect(calls).toBe(1);
});

it("replacement parks edited composition durably instead of relabeling unrelated replacement text", () => {
	document.appendEditedRecovery(recovery());
	const original = repository.read(destination);
	document.replaceDraft("replacement");
	reopen();
	expect(document.getSnapshot().record).toMatchObject({
		draft: "replacement",
		unconfirmed: original.draft,
		unconfirmedImages: original.images,
		unconfirmedComposer: original.composer,
	});
	expect(document.getSnapshot().record.composer).toBeUndefined();
	expect(document.imagePreviews(document.getSnapshot().record.unconfirmedImages).map((i) => i.data)).toEqual(["AP8H"]);
	document.dismiss();
	document.restore();
	expect(repository.read(destination).unconfirmedComposer?.recoveryIds).toEqual(["source-a"]);
	expect(document.restoreEditedRecovery([recovery()])).toBe(true);
});

it.each(["dismiss", "restore"] as const)(
	"%s cannot discard or replay uncertain edited ownership without retained-source proof",
	async (action) => {
		document.appendEditedRecovery(recovery());
		await document.submit(async () => false);
		const before = repository.read(destination);
		document[action]();
		reopen();
		expect(document.getSnapshot().record).toEqual(before);
	},
);

it("submitText preserves current edited ownership and acknowledgment housekeeping through ordinary uncertainty", async () => {
	repository.write(destination, { draft: "", unconfirmed: null, recoveryResendAcks: ["pending-ack"] });
	reopen();
	document.appendEditedRecovery(recovery("source-a", { composerText: "[image 9] /recovered-skill" }));
	const original = repository.read(destination);
	await document.submitText("independent", async () => false);
	reopen();
	expect(document.getSnapshot().record).toMatchObject({ ...original, unconfirmed: "independent" });
	document.edit("");
	document.restore();
	expect(repository.read(destination).unconfirmed).toBe("independent");
	document.dismiss();
	reopen();
	expect(document.getSnapshot().record).toMatchObject({
		draft: "",
		unconfirmed: null,
		composer: { ...original.composer, skillNames: [], mentions: [] },
		images: original.images,
		recoveryResendAcks: ["pending-ack"],
	});
});

it("ordinary restore and replacement retain acknowledgment work and ordinary recovered selections", async () => {
	repository.write(destination, {
		draft: "",
		unconfirmed: "uncertain",
		unconfirmedComposer: { skillNames: ["ordinary"] },
		recoveryResendAcks: ["pending-ack"],
	});
	reopen();
	document.restore();
	reopen();
	expect(document.getSnapshot().record).toMatchObject({
		draft: "uncertain",
		unconfirmed: null,
		composer: { skillNames: ["ordinary"] },
		recoveryResendAcks: ["pending-ack"],
	});
	document.replaceDraft("replacement");
	reopen();
	expect(document.getSnapshot().record).toEqual({
		draft: "replacement",
		unconfirmed: null,
		recoveryResendAcks: ["pending-ack"],
	});
	await document.submitText("independent", async () => true);
	expect(repository.read(destination).recoveryResendAcks).toEqual(["pending-ack"]);
});

it("rejected text restore preserves independent captured ownership and rejects selection-only or recovery-only composers", () => {
	repository.write(destination, {
		draft: "",
		unconfirmed: "edited",
		unconfirmedComposer: { recoveryIds: ["a"] },
		recoveryResendAcks: ["pending-ack"],
	});
	reopen();
	expect(document.restoreRecoveredDraft("rejected")).toBe(true);
	reopen();
	expect(document.getSnapshot().record).toEqual({
		draft: "rejected",
		unconfirmed: "edited",
		unconfirmedComposer: { recoveryIds: ["a"] },
		recoveryResendAcks: ["pending-ack"],
	});
	for (const composer of [{ skillNames: ["skill"] }, { commandNames: ["command"] }, { recoveryIds: ["a"] }]) {
		repository.write(destination, { draft: "", unconfirmed: null, composer });
		reopen();
		expect(document.canRestoreRecoveredDraft()).toBe(false);
		expect(document.recoveredRestoreBlock()).toBe("occupied");
		expect(document.restoreRecoveredDraft("rejected")).toBe(false);
		expect(repository.read(destination).composer).toEqual(composer);
	}
});

it("retains actual mutation recovery rows through failed and successful append until explicit atomic resend", async () => {
	const opened = openSqliteSyncDouble(join(directory, "mutations.sqlite"));
	let nextId = 0;
	const storage = new MutationOutboxSQLite(opened.port, {
		randomSource: {},
		createMutationId: () => `mutation-${++nextId}`,
	});
	try {
		const queued = await storage.enqueueIntent(recovery());
		const source = await storage.editQueuedMessage(queued, { stopEpoch: 0 });
		expect(source).toBeDefined();
		if (!source) throw new Error("Missing claimed source");
		document.edit("prior");
		database.exec("PRAGMA query_only = ON");
		expect(document.appendEditedRecovery(source)).toBe("blocked");
		expect(await storage.getRecovery(source.clientMutationId)).toEqual(source);
		expect(repository.read(destination)).toEqual({ draft: "prior", unconfirmed: null });
		database.exec("PRAGMA query_only = OFF");
		document.retry();
		expect(document.appendEditedRecovery(source)).toBe("restored");
		reopen();
		expect(await storage.getRecovery(source.clientMutationId)).toEqual(source);
		await document.submit(async (_text, _images, context) => {
			expect(repository.read(destination).unconfirmedComposer?.recoveryIds).toEqual([source.clientMutationId]);
			const fresh = await storage.resendRecovery(context.recoverySourceIds ?? [], {
				...source,
				composerText: context.composer?.text,
			});
			expect(fresh?.clientMutationId).not.toBe(source.clientMutationId);
			return !!fresh;
		});
		expect(await storage.getRecovery(source.clientMutationId)).toBeUndefined();
		reopen();
		const receipt = await storage.readRecoveryResend(targetRef, [source.clientMutationId]);
		expect(receipt).toBeDefined();
		if (!receipt) throw new Error("Missing receipt");
		expect(document.settleEditedResend(receipt)).toBe(true);
		reopen();
		expect(document.getSnapshot().record.recoveryResendAcks).toEqual([receipt.clientMutationId]);
		await storage.acknowledgeRecoveryResend(targetRef, receipt.clientMutationId);
		expect(document.finishRecoveryResendAck(receipt.clientMutationId)).toBe(true);
		expect(await storage.readRecoveryResend(targetRef, [source.clientMutationId])).toBeUndefined();
	} finally {
		opened.database.close();
	}
});

it("remaps recovered multi-digit markers and UTF-16 mention offsets without changing original bytes", () => {
	document.edit("😀");
	expect(
		document.appendEditedRecovery(
			recovery("wide", {
				composerText: "[image 12] tail",
				composerMentions: [{ kind: "skill", name: "recovered-skill", offset: 11 }],
				attachments: [{ presentationId: "original", marker: 12, name: "photo.png", mediaType: "image/png" }],
			}),
		),
	).toBe("restored");
	reopen();
	expect(document.getSnapshot().record.draft).toBe("😀\n\n[image 1] tail");
	expect(document.getSnapshot().record.composer?.mentions).toEqual([
		{ kind: "skill", name: "recovered-skill", offset: 14 },
	]);
	expect(document.imagePreviews()[0]).toEqual({ marker: 1, name: "photo.png", mediaType: "image/png", data: "AP8H" });
});

it("keeps acknowledgments independent and forget only releases memory ownership", async () => {
	repository.write(destination, { draft: "", unconfirmed: null, recoveryResendAcks: ["older"] });
	reopen();
	document.appendEditedRecovery(recovery());
	await document.submit(async () => true);
	expect(document.settleEditedResend({ targetRef, sourceIds: ["source-a"], clientMutationId: "newer" })).toBe(true);
	expect(document.finishRecoveryResendAck("older")).toBe(true);
	expect(repository.read(destination).recoveryResendAcks).toEqual(["newer"]);
	document.appendEditedRecovery(recovery("next"));
	const before = repository.read(destination);
	document.forget();
	document.retry();
	expect(document.appendEditedRecovery(recovery())).toBe("blocked");
	expect(document.finishRecoveryResendAck("newer")).toBe(false);
	reopen();
	expect(document.getSnapshot().record).toEqual(before);
});

it("failed replacement retains original durable recovery and can save the parked revision on retry", () => {
	document.appendEditedRecovery(recovery());
	const original = repository.read(destination);
	database.exec("PRAGMA query_only = ON");
	document.replaceDraft("replacement");
	expect(repository.read(destination)).toEqual(original);
	expect(document.getSnapshot().record.unconfirmedComposer?.recoveryIds).toEqual(["source-a"]);
	database.exec("PRAGMA query_only = OFF");
	document.retry();
	reopen();
	expect(document.getSnapshot().record).toMatchObject({
		draft: "replacement",
		unconfirmed: original.draft,
		unconfirmedComposer: original.composer,
	});
	expect(document.imagePreviews(document.getSnapshot().record.unconfirmedImages)[0]?.data).toBe("AP8H");
});

it("failed emptying keeps recovery ownership until the release save commits", () => {
	document.appendEditedRecovery(
		recovery("source-a", {
			payload: { input: [{ type: "text", text: "encoded" }] },
			attachments: [],
			composerText: "encoded",
			composerMentions: undefined,
		}),
	);
	database.exec("PRAGMA query_only = ON");
	document.edit("");
	expect(document.getSnapshot().record.composer?.recoveryIds).toEqual(["source-a"]);
	expect(document.getSnapshot().record.releasedRecoveryIds ?? []).not.toContain("source-a");
	database.exec("PRAGMA query_only = OFF");
	document.retry();
	expect(document.getSnapshot().record.composer?.recoveryIds ?? []).toEqual([]);
	expect(document.getSnapshot().record.releasedRecoveryIds).toContain("source-a");
});

it("ordinary uncertain restoration does not overwrite image-free selection or recovery-only current work", () => {
	for (const composer of [{ skillNames: ["s"] }, { commandNames: ["c"] }, { recoveryIds: ["r"] }]) {
		const original = { draft: "", unconfirmed: "ordinary", composer, recoveryResendAcks: ["ack"] };
		repository.write(destination, original);
		reopen();
		document.restore();
		expect(repository.read(destination)).toEqual(original);
	}
});
