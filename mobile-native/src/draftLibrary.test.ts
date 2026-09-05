import { describe, expect, it } from "vitest";
import { DraftLibrary } from "./draftLibrary";
import type { DraftRecord } from "./draftRepository";

function setup() {
	const records = new Map<string, DraftRecord>();
	const repository = {
		read: ({ hubId, sessionRef }: { hubId: string; sessionRef: string }) =>
			records.get(JSON.stringify([hubId, sessionRef])) ?? {
				draft: "",
				unconfirmed: null,
			},
		write: (
			{ hubId, sessionRef }: { hubId: string; sessionRef: string },
			record: DraftRecord,
		) => {
			records.set(JSON.stringify([hubId, sessionRef]), record);
		},
		removeHub: (hubId: string) => {
			for (const key of records.keys())
				if (JSON.parse(key)[0] === hubId) records.delete(key);
		},
	};
	return { library: new DraftLibrary(() => repository), repository };
}

describe("drafts across navigation", () => {
	it("invalidates open documents even if deleting persisted data fails", () => {
		const { library, repository } = setup();
		const destination = { hubId: "one", sessionRef: "session" };
		const document = library.open(destination);
		document.edit("saved");
		repository.removeHub = () => {
			throw new Error("disk unavailable");
		};
		expect(() => library.removeHub("one")).toThrow();
		document.edit("must not write");
		expect(repository.read(destination).draft).toBe("saved");
		expect(document.getSnapshot().loaded).toBe(false);
	});
	it("reuses a destination document so a late acceptance preserves reopened input", async () => {
		const { library } = setup();
		const destination = { hubId: "one", sessionRef: "session" };
		const original = library.open(destination);
		original.edit("sent");
		await original.submit(async () => {
			library.open(destination).edit("composed after returning");
			return true;
		});
		expect(library.open(destination).getSnapshot().record).toEqual({
			draft: "composed after returning",
			unconfirmed: null,
		});
		expect(
			library.open({ ...destination, hubId: "two" }).getSnapshot().record.draft,
		).toBe("");
	});

	it("does not let a late completion or stale screen recreate a removed hub's drafts", async () => {
		const { library, repository } = setup();
		const destination = { hubId: "one", sessionRef: "session" };
		const document = library.open(destination);
		document.edit("sent");
		await document.submit(async () => {
			library.removeHub("one");
			document.edit("stale input");
			document.retry();
			return true;
		});
		expect(repository.read(destination)).toEqual({
			draft: "",
			unconfirmed: null,
		});
		expect(library.open(destination)).not.toBe(document);
		expect(library.open(destination).getSnapshot().record.draft).toBe("");
	});
});
