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

const destination = { hubId: "hub-a", sessionRef: "session-1" };

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
