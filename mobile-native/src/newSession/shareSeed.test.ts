import { expect, it } from "vitest";
import { CreationDraftRepository } from "../creationDraftRepository";
import { createNewSessionStore, type DraftStorage } from "../newSession";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import { LaunchMemory } from "./launchMemory";
import { memoryStorage } from "./newSessionTestUtils";
import { seedSharedPrompt } from "./shareSeed";

const HUB = "hub-1";

/** The New session sheet's storage, with a switch for a storage that refuses
 * writes (the disk-full case the form reports). */
function fixture() {
	const { database, port } = openSqliteSyncDouble();
	const drafts = new CreationDraftRepository(port);
	const control = { failWrite: false };
	const storage: DraftStorage = () => ({
		read: (hubId) => drafts.read(hubId),
		write: (hubId, draft) => {
			if (control.failWrite) throw new Error("disk full");
			drafts.write(hubId, draft);
		},
		clear: (hubId) => drafts.clear(hubId),
	});
	return { storage, control, close: () => database.close() };
}

function opening() {
	const environment = fixture();
	const store = createNewSessionStore(HUB, environment.storage);
	const memory = new LaunchMemory(memoryStorage(), HUB);
	return { ...environment, store, memory };
}

it("applies the remembered setup when the form opens empty, then appends the shared text", () => {
	const environment = opening();
	environment.memory.recordStart({ host: "local", cwd: "/project", model: null, effort: "", overrides: {} });
	try {
		expect(seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "What I shared" }).saved).toBe(
			true,
		);
		const form = environment.store.getState();
		expect(form.cwd).toBe("/project");
		expect(form.prompt).toBe("What I shared");
	} finally {
		environment.close();
	}
});

it("keeps a saved draft's prompt, as New session already does, and appends to it", () => {
	const environment = opening();
	environment.store.getState().setPrompt("What I was writing");
	environment.memory.recordStart({ host: "local", cwd: "/project", model: null, effort: "", overrides: {} });
	try {
		expect(seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "What I shared" }).saved).toBe(
			true,
		);
		const form = environment.store.getState();
		expect(form.cwd).toBe("");
		expect(form.prompt).toBe("What I was writing\n\nWhat I shared");
	} finally {
		environment.close();
	}
});

it("places the shared text alone when the saved prompt was empty", () => {
	const environment = opening();
	try {
		expect(seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "What I shared" }).saved).toBe(
			true,
		);
		expect(environment.store.getState().prompt).toBe("What I shared");
	} finally {
		environment.close();
	}
});

it("reports the seed did not land when the draft cannot be saved", () => {
	const environment = opening();
	try {
		environment.control.failWrite = true;
		expect(seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "What I shared" }).saved).toBe(
			false,
		);
		expect(environment.store.getState().storageError).not.toBeNull();
	} finally {
		environment.close();
	}
});
