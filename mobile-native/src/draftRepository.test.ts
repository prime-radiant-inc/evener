import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { AskQuestionRef } from "@evener/appwire-client";
import {
	boundQuestionText,
	questionsIdentity,
} from "./questionAnswers";
import {
	boundQuestion,
	MAX_ITEM_BYTES,
} from "./projectedRows";
import { DraftRepository } from "./draftRepository";
import { openSqliteSyncDouble, type SqliteDoubleDatabase } from "./sqliteSync.testkit";

let directory: string;
let database: SqliteDoubleDatabase;
let repository: DraftRepository;

function openRepository() {
	const opened = openSqliteSyncDouble(join(directory, "drafts.sqlite"));
	database = opened.database;
	repository = new DraftRepository(opened.port);
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
// questionsIdentity (questionAnswers.ts) is the SIGNATURE writeQuestions/
// readQuestions actually receive from QuestionSheet.tsx in production — not
// the hand-built JSON-array literals the other cases in this file use.
// questionDefinitions (draftRepository.ts) parses that signature expecting
// an array it can index per key; an identity that returns anything else
// (a bare hash string, #1731 piece E round 4's own High) throws on every
// write/read, and question drafts never persist.
test("questionsIdentity's own signature round-trips through the real repository", () => {
	const question: AskQuestionRef = {
		key: "call:0",
		callId: "call",
		header: "Choice",
		question: "Choose",
		multiSelect: false,
		options: [{ label: "A", detail: "" }],
	};
	const signature = questionsIdentity([question]);
	const selections = {
		[question.key]: {
			resolution: { kind: "option" as const, labels: ["A"] },
			note: "",
		},
	};
	repository.writeQuestions(destination, signature, selections);
	database.close();
	openRepository();
	expect(repository.readQuestions(destination, signature)).toEqual(
		selections,
	);
	// A genuinely different question set gets a different identity and does
	// not inherit the prior answer.
	const changed = questionsIdentity([{ ...question, question: "Changed" }]);
	expect(changed).not.toBe(signature);
	expect(repository.readQuestions(destination, changed)).toEqual({});
});

// Before questionsIdentity signed question drafts (the full-question
// JSON.stringify signature an older build's sheet persisted), a saved answer
// must still load once the identity's {key, digest} signature replaces it:
// same canonical question, different era's persisted shape. The repository
// normalizes both forms to one comparable definition, so the upgrade never
// silently drops a reader's saved answers.
test("a draft saved under the pre-identity full-question signature still loads through questionsIdentity", () => {
	const question: AskQuestionRef = {
		key: "call:0",
		callId: "call",
		header: "Choice",
		question: "Choose",
		multiSelect: false,
		options: [{ label: "A", detail: "" }],
	};
	const selections = {
		[question.key]: {
			resolution: { kind: "option" as const, labels: ["A"] },
			note: "",
		},
	};
	repository.writeQuestions(
		destination,
		JSON.stringify([question]),
		selections,
	);
	database.close();
	openRepository();
	expect(
		repository.readQuestions(destination, questionsIdentity([question])),
	).toEqual(selections);
	// A genuinely different question still does not inherit the draft.
	expect(
		repository.readQuestions(
			destination,
			questionsIdentity([{ ...question, question: "Changed" }]),
		),
	).toEqual({});
});

// Older still than the full-question signature above: the sheet's questions
// once came from the bounded timeline rows themselves (the pre-identity
// pendingQuestions read conversation.items), so a draft saved in that window
// holds an oversized question's TRUNCATED copy as its definition — the cut
// copy hashes to a digest the identity never computes for the canonical
// question. The comparison must accept that bounded digest too, or the
// upgrade silently drops a reader's saved answer for exactly the questions
// too large to fit a timeline row.
test("a draft saved under the pre-identity bounded-copy signature still loads through questionsIdentity", () => {
	const huge = "x".repeat(MAX_ITEM_BYTES + 10);
	const oversized: AskQuestionRef = {
		key: "call:0",
		callId: "call",
		header: huge,
		question: huge,
		multiSelect: false,
		options: [{ label: huge, detail: huge }],
	};
	const selections = {
		[oversized.key]: {
			resolution: { kind: "free" as const, text: "custom answer" },
			note: "context",
		},
	};
	repository.writeQuestions(
		destination,
		JSON.stringify([boundQuestion(oversized, boundQuestionText)]),
		selections,
	);
	database.close();
	openRepository();
	expect(
		repository.readQuestions(destination, questionsIdentity([oversized])),
	).toEqual(selections);
	// A genuinely different question still does not inherit the draft.
	expect(
		repository.readQuestions(
			destination,
			questionsIdentity([{ ...oversized, header: `different ${huge}` }]),
		),
	).toEqual({});
});

// The bounded digest inherits the persisted era's one blind spot by design:
// a stored truncated copy cannot tell whether the agent changed the question
// past the display bound — the untruncated text is gone from the row — so a
// draft saved under the bounded signature loads for a same-key question that
// differs only beyond it, exactly as it did under the signature era that
// persisted it. The first save rewrites the definition canonically, so the
// window closes on the reader's first edit.
test("a legacy bounded draft loads for a same-key question that differs only past the display bound", () => {
	const huge = "x".repeat(MAX_ITEM_BYTES + 10);
	const first: AskQuestionRef = {
		key: "call:0",
		callId: "call",
		header: huge,
		question: "Choose",
		multiSelect: false,
		options: [{ label: "A", detail: "" }],
	};
	const second = { ...first, header: `${huge}-and-more` };
	const selections = {
		[first.key]: {
			resolution: { kind: "free" as const, text: "kept" },
			note: "",
		},
	};
	repository.writeQuestions(
		destination,
		JSON.stringify([boundQuestion(first, boundQuestionText)]),
		selections,
	);
	database.close();
	openRepository();
	expect(
		repository.readQuestions(destination, questionsIdentity([second])),
	).toEqual(selections);
	// The reader's first save rewrites the stored definition canonically: the
	// draft now follows the current question's own digest, and the superseded
	// question no longer matches either stored digest.
	const edited = {
		[first.key]: {
			resolution: { kind: "free" as const, text: "edited" },
			note: "",
		},
	};
	repository.writeQuestions(destination, questionsIdentity([second]), edited);
	expect(
		repository.readQuestions(destination, questionsIdentity([second])),
	).toEqual(edited);
	expect(
		repository.readQuestions(destination, questionsIdentity([first])),
	).toEqual({});
});

// A window of the display-bound work persisted a third shape: the sheet's
// questions were the rows project.ts built with a nested display twin
// (d0f40080cb's MobileQuestionRef) — the wire's canonical fields whole, beside
// a bounded copy of the same prose the store's display bound cut for the
// reader — and a draft saved in that window holds that whole wrapper as its
// definition. The twin is not part of the question the identity signs, so
// hashing the stored element as-persisted yields a digest the identity never
// computes for the same question, and the saved answer is silently dropped
// on upgrade. The element below is the era's own construction:
// withQuestionDisplay over the package's canonical ref, the twin carrying
// the bound's cut copies.
test("a draft saved under the display-twin signature still loads through questionsIdentity", () => {
	const huge = "x".repeat(MAX_ITEM_BYTES + 10);
	const canonical: AskQuestionRef = {
		key: "call:0",
		callId: "call",
		header: huge,
		question: "Choose",
		multiSelect: false,
		options: [{ label: "A", detail: "", recommended: false }],
	};
	const displayEra = {
		...canonical,
		display: {
			header: boundQuestionText(canonical.header),
			question: canonical.question,
			options: [{ label: "A", detail: "" }],
		},
	};
	const selections = {
		[canonical.key]: {
			resolution: { kind: "free" as const, text: "display-era answer" },
			note: "context",
		},
	};
	repository.writeQuestions(
		destination,
		JSON.stringify([displayEra]),
		selections,
	);
	database.close();
	openRepository();
	expect(
		repository.readQuestions(destination, questionsIdentity([canonical])),
	).toEqual(selections);
	// A genuinely different question still does not inherit the draft.
	expect(
		repository.readQuestions(
			destination,
			questionsIdentity([{ ...canonical, header: `different ${huge}` }]),
		),
	).toEqual({});
});

// The full-question eras each serialized the same canonical question in a
// different FIELD ORDER than the package ref the identity hashes today, and
// a digest over the raw serialization inherits that order. The landed
// checkpoint (#1096) built its questions key-first and left callId to
// pendingQuestions' spread, which appended it LAST — a draft saved by a
// build of that era holds that order as its definition, and hashing it
// as-persisted yields a digest the identity never computes for the same
// question. The element below is the checkpoint's construction: the
// projection's key-first question with the callId pendingQuestions spread on.
test("a draft saved under the checkpoint's key-first signature still loads through questionsIdentity", () => {
	const canonical: AskQuestionRef = {
		key: "call:0",
		callId: "call",
		header: "Choice",
		question: "Choose",
		multiSelect: false,
		options: [{ label: "A", detail: "", recommended: false }],
	};
	const checkpointEra = {
		key: canonical.key,
		header: canonical.header,
		question: canonical.question,
		options: canonical.options,
		multiSelect: canonical.multiSelect,
		callId: canonical.callId,
	};
	const selections = {
		[canonical.key]: {
			resolution: { kind: "option" as const, labels: ["A"] },
			note: "",
		},
	};
	repository.writeQuestions(
		destination,
		JSON.stringify([checkpointEra]),
		selections,
	);
	database.close();
	openRepository();
	expect(
		repository.readQuestions(destination, questionsIdentity([canonical])),
	).toEqual(selections);
	// A genuinely different question still does not inherit the draft.
	expect(
		repository.readQuestions(
			destination,
			questionsIdentity([{ ...canonical, question: "Changed" }]),
		),
	).toEqual({});
});

// Between the checkpoint and the projection cutover, #1488's shim built each
// row's question from the parsed wire fields and appended key and callId
// AFTER them (parsed.map((q, idx) => ({ ...q, key, callId }))), so a draft
// saved by a build of that era serializes the same canonical question
// header-first. The same era family as the two above — a different byte
// order of the same fields, the same silent drop — so the same
// normalization must cover it.
test("a draft saved under the shim-era parsed-fields-first signature still loads through questionsIdentity", () => {
	const canonical: AskQuestionRef = {
		key: "call:0",
		callId: "call",
		header: "Choice",
		question: "Choose",
		multiSelect: false,
		options: [{ label: "A", detail: "", recommended: false }],
	};
	const shimEra = {
		header: canonical.header,
		question: canonical.question,
		options: canonical.options,
		multiSelect: canonical.multiSelect,
		key: canonical.key,
		callId: canonical.callId,
	};
	const selections = {
		[canonical.key]: {
			resolution: { kind: "option" as const, labels: ["A"] },
			note: "",
		},
	};
	repository.writeQuestions(destination, JSON.stringify([shimEra]), selections);
	database.close();
	openRepository();
	expect(
		repository.readQuestions(destination, questionsIdentity([canonical])),
	).toEqual(selections);
	// A genuinely different question still does not inherit the draft.
	expect(
		repository.readQuestions(
			destination,
			questionsIdentity([{ ...canonical, question: "Changed" }]),
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
