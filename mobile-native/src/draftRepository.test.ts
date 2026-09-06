import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { afterEach, beforeEach, expect, test } from "vitest";
import { type DraftDatabase, DraftRepository } from "./draftRepository";

let directory: string;
let database: DatabaseSync;
let repository: DraftRepository;

function openRepository() {
	database = new DatabaseSync(join(directory, "drafts.sqlite"));
	const adapter: DraftDatabase = {
		execSync: (sql) => database.exec(sql),
		runSync: (sql, ...params) => database.prepare(sql).run(...params),
		getFirstSync: <T>(sql: string, ...params: string[]) =>
			(database.prepare(sql).get(...params) as T | undefined) ?? null,
	};
	repository = new DraftRepository(adapter);
}

beforeEach(() => {
	directory = mkdtempSync(join(tmpdir(), "evener-drafts-"));
	openRepository();
});

afterEach(() => {
	database.close();
	rmSync(directory, { recursive: true, force: true });
});

const questionSignature = JSON.stringify([
	{
		key: "call:0",
		header: "Question",
		question: "Choose",
		options: [{ label: "A", detail: "" }],
		multiSelect: false,
	},
]);
const changedSignature = JSON.stringify([
	{
		key: "call:0",
		header: "Question",
		question: "Changed",
		options: [{ label: "A", detail: "" }],
		multiSelect: false,
	},
]);
const destination = { hubId: "hub-a", sessionRef: "session-1" };

test("image bytes and draft references survive reopen without following another hub", () => {
	const image = {
		id: "image-a",
		marker: 3,
		mediaType: "image/png",
		name: "proof.png",
	};
	repository.write(
		destination,
		{ draft: "[image 3]", unconfirmed: null, images: [image] },
		[{ ...image, data: "AQID" }],
	);
	database.close();
	openRepository();
	expect(repository.read(destination).images).toEqual([image]);
	expect(repository.imageInputs(destination, [image])).toEqual([
		{ marker: 3, mediaType: "image/png", name: "proof.png", data: "AQID" },
	]);
	expect(
		repository.read({ ...destination, hubId: "hub-b" }).images,
	).toBeUndefined();
	expect(() =>
		repository.imageInputs({ ...destination, hubId: "hub-b" }, [image]),
	).toThrow();
});

test("text edits do not rewrite image bytes and a failed checkpoint rolls back references", () => {
	const image = { id: "image-a", marker: 1, mediaType: "image/png" };
	const record = { draft: "before", unconfirmed: null, images: [image] };
	repository.write(destination, record, [{ ...image, data: "AQID" }]);
	database.exec(
		"CREATE TRIGGER immutable_image BEFORE UPDATE ON draft_images BEGIN SELECT RAISE(ABORT, 'immutable image'); END",
	);
	repository.write(destination, { ...record, draft: "after" });
	database.exec(
		"CREATE TRIGGER reject_checkpoint BEFORE UPDATE ON drafts BEGIN SELECT RAISE(ABORT, 'checkpoint rejected'); END",
	);
	expect(() =>
		repository.write(destination, {
			draft: "",
			unconfirmed: "after",
			unconfirmedImages: [image],
		}),
	).toThrow();
	expect(repository.read(destination)).toEqual({ ...record, draft: "after" });
	expect(repository.imageInputs(destination, [image])[0]?.data).toBe("AQID");
});

test("retains uncertain images and deletes bytes only after their final reference is cleared", () => {
	const image = { id: "image-a", marker: 1, mediaType: "image/png" };
	repository.write(
		destination,
		{ draft: "", unconfirmed: "[image 1]", unconfirmedImages: [image] },
		[{ ...image, data: "AQID" }],
	);
	expect(repository.imageInputs(destination, [image])).toHaveLength(1);
	repository.write(destination, { draft: "", unconfirmed: null });
	expect(() => repository.imageInputs(destination, [image])).toThrow();
	expect(() =>
		repository.write(destination, {
			draft: "",
			unconfirmed: null,
			images: [image],
		}),
	).toThrow();
});

test("rejects replacing immutable image identity and hub removal only clears that hub's bytes", () => {
	const image = { id: "same-id", marker: 1, mediaType: "image/png" };
	const otherHub = { ...destination, hubId: "other" };
	const record = { draft: "", unconfirmed: null, images: [image] };
	repository.write(destination, record, [{ ...image, data: "AQID" }]);
	repository.write(otherHub, record, [{ ...image, data: "BAUG" }]);
	expect(() =>
		repository.write(destination, record, [{ ...image, data: "replaced" }]),
	).toThrow();
	expect(repository.imageInputs(destination, [image])[0]?.data).toBe("AQID");
	repository.removeHub(destination.hubId);
	expect(() => repository.imageInputs(destination, [image])).toThrow();
	expect(repository.imageInputs(otherHub, [image])[0]?.data).toBe("BAUG");
});

test("a failure after adding bytes rolls back the bytes and the new reference together", () => {
	const image = { id: "photo", marker: 1, mediaType: "image/png" };
	database.exec(
		"CREATE TRIGGER reject_text BEFORE INSERT ON drafts BEGIN SELECT RAISE(ABORT, 'text rejected'); END",
	);
	expect(() =>
		repository.write(
			destination,
			{ draft: "image", unconfirmed: null, images: [image] },
			[{ ...image, data: "AQID" }],
		),
	).toThrow();
	expect(repository.read(destination)).toEqual({
		draft: "",
		unconfirmed: null,
	});
	expect(() => repository.imageInputs(destination, [image])).toThrow();
});

test("missing drafts read as empty without unconfirmed text", () => {
	expect(repository.read(destination)).toEqual({
		draft: "",
		unconfirmed: null,
	});
});

test("draft and unconfirmed text survive database close and reopen", () => {
	const draft = "🦋 日本語 é\n".repeat(20_000);
	repository.write(destination, { draft, unconfirmed: "possibly sent" });
	database.close();
	openRepository();
	expect(repository.read(destination)).toEqual({
		draft,
		unconfirmed: "possibly sent",
	});
});

test("writing replaces the destination and preserves empty unconfirmed text", () => {
	repository.write(destination, { draft: "before", unconfirmed: "pending" });
	repository.write(destination, { draft: "after", unconfirmed: "" });
	expect(repository.read(destination)).toEqual({
		draft: "after",
		unconfirmed: "",
	});
	repository.write(destination, { draft: "after", unconfirmed: null });
	expect(repository.read(destination)).toEqual({
		draft: "after",
		unconfirmed: null,
	});
});

test("clearing a destination leaves other sessions and hubs intact", () => {
	const otherHub = { ...destination, hubId: "hub-b" };
	const otherSession = { ...destination, sessionRef: "session-2" };
	repository.write(destination, { draft: "clear me", unconfirmed: "pending" });
	repository.write(otherHub, { draft: "other hub", unconfirmed: null });
	repository.write(otherSession, { draft: "other session", unconfirmed: null });
	repository.write(destination, { draft: "", unconfirmed: null });
	expect(repository.read(destination)).toEqual({
		draft: "",
		unconfirmed: null,
	});
	expect(repository.read(otherHub)).toEqual({
		draft: "other hub",
		unconfirmed: null,
	});
	expect(repository.read(otherSession)).toEqual({
		draft: "other session",
		unconfirmed: null,
	});
});

test("removing a hub clears all its destinations and retains other hubs", () => {
	const otherSession = { ...destination, sessionRef: "session-2" };
	const otherHub = { ...destination, hubId: "hub-b" };
	for (const target of [destination, otherSession, otherHub]) {
		repository.write(target, { draft: "retained", unconfirmed: "pending" });
	}
	repository.removeHub(destination.hubId);
	database.close();
	openRepository();
	expect(repository.read(destination)).toEqual({
		draft: "",
		unconfirmed: null,
	});
	expect(repository.read(otherSession)).toEqual({
		draft: "",
		unconfirmed: null,
	});
	expect(repository.read(otherHub)).toEqual({
		draft: "retained",
		unconfirmed: "pending",
	});
});

test("SQL-like text in identifiers and content remains literal data", () => {
	const unusual = {
		hubId: "hub'); DROP TABLE drafts; --",
		sessionRef: "' OR 1=1 --",
	};
	const record = {
		draft: "'); DELETE FROM drafts; --",
		unconfirmed: "' OR '1'='1",
	};
	repository.write(destination, { draft: "safe", unconfirmed: null });
	repository.write(unusual, record);
	expect(repository.read(unusual)).toEqual(record);
	repository.removeHub(unusual.hubId);
	expect(repository.read(destination)).toEqual({
		draft: "safe",
		unconfirmed: null,
	});
});

test("question selections survive reopening only for the exact destination and questions", () => {
	const selections = {
		"call:0": {
			resolution: { kind: "free" as const, text: "custom 🦋" },
			note: "context",
		},
	};
	repository.writeQuestions(destination, questionSignature, selections);
	database.close();
	openRepository();
	expect(repository.readQuestions(destination, questionSignature)).toEqual(
		selections,
	);
	expect(repository.readQuestions(destination, changedSignature)).toEqual({});
	expect(
		repository.readQuestions(
			{ ...destination, hubId: "other" },
			questionSignature,
		),
	).toEqual({});
	expect(
		repository.readQuestions(
			{ ...destination, sessionRef: "other" },
			questionSignature,
		),
	).toEqual({});
});
test("hub removal also clears question selections without affecting another hub", () => {
	const other = { ...destination, hubId: "other" };
	const selections = {
		"call:0": { resolution: { kind: "skip" as const }, note: "" },
	};
	for (const target of [destination, other])
		repository.writeQuestions(target, questionSignature, selections);
	repository.removeHub(destination.hubId);
	expect(repository.readQuestions(destination, questionSignature)).toEqual({});
	expect(repository.readQuestions(other, questionSignature)).toEqual(
		selections,
	);
});
test("malformed question selections report corruption instead of enabling submission", () => {
	repository.writeQuestions(destination, questionSignature, {});
	database
		.prepare("UPDATE question_drafts SET selections = ?")
		.run('{"call:0":{"note":"","resolution":{"kind":"option","labels":42}}}');
	expect(() =>
		repository.readQuestions(destination, questionSignature),
	).toThrow();
});

test("adding questions and writing a sibling batch preserves unfinished answers", () => {
	const first = {
		key: "first:0",
		header: "First",
		question: "Choose",
		multiSelect: false,
		options: [{ label: "A", detail: "" }],
	};
	const second = { ...first, key: "second:0", header: "Second" };
	const a = {
		[first.key]: {
			resolution: { kind: "free" as const, text: "unfinished" },
			note: "keep",
		},
	};
	const b = { [second.key]: { resolution: null, note: "second note" } };
	repository.writeQuestions(destination, JSON.stringify([first]), a);
	expect(
		repository.readQuestions(destination, JSON.stringify([first, second])),
	).toEqual(a);
	repository.writeQuestions(destination, JSON.stringify([second]), b);
	database.close();
	openRepository();
	expect(
		repository.readQuestions(destination, JSON.stringify([first, second])),
	).toEqual({ ...a, ...b });
	expect(
		repository.readQuestions(
			destination,
			JSON.stringify([{ ...first, question: "Changed" }, second]),
		),
	).toEqual(b);
});

test("active question survives reopen and stays within its destination and pending set", () => {
	repository.writeQuestionPosition(destination, "second:0");
	database.close();
	openRepository();
	expect(
		repository.readQuestionPosition(destination, ["first:0", "second:0"]),
	).toBe("second:0");
	expect(repository.readQuestionPosition(destination, ["replacement:0"])).toBe(
		"replacement:0",
	);
	expect(
		repository.readQuestionPosition({ ...destination, hubId: "other" }, [
			"first:0",
			"second:0",
		]),
	).toBe("first:0");
	expect(
		repository.readQuestionPosition({ ...destination, sessionRef: "other" }, [
			"first:0",
			"second:0",
		]),
	).toBe("first:0");
	repository.removeHub(destination.hubId);
	expect(
		repository.readQuestionPosition(destination, ["first:0", "second:0"]),
	).toBe("first:0");
});
