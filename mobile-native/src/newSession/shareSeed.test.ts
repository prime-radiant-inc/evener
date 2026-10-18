import { expect, it } from "vitest";
import { CreationDraftRepository } from "../creationDraftRepository";
import { createNewSessionStore, type DraftStorage } from "../newSession";
import { openSqliteSyncDouble } from "../sqliteSync.testkit";
import { LaunchMemory } from "./launchMemory";
import { memoryStorage } from "./newSessionTestUtils";
import { retrySharedSeed, seedSharedPrompt, shareSeedLanded } from "./shareSeed";

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

it("reports the prompt it seeded, so a later check can tell it landed", () => {
	const environment = opening();
	try {
		const seed = seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "What I shared" });
		expect(seed.text).toBe("What I shared");
		expect(shareSeedLanded(environment.store, seed.text)).toBe(true);
		expect(shareSeedLanded(environment.store, "Something else")).toBe(false);
	} finally {
		environment.close();
	}
});

it("does not append the same share twice when the sheet is reopened on it", () => {
	const environment = opening();
	try {
		seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "  What I shared\n" });
		seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "  What I shared\n" });
		expect(environment.store.getState().prompt).toBe("What I shared");
	} finally {
		environment.close();
	}
});

it("reports a landing after the text was edited around, without appending it again", () => {
	const environment = opening();
	try {
		const seed = seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "What I shared" });
		environment.store.getState().setPrompt(`${seed.text} and more I typed`);
		expect(shareSeedLanded(environment.store, seed.text)).toBe(true);
		expect(retrySharedSeed(environment.store, seed.text)).toBe(true);
		expect(environment.store.getState().prompt).toBe("What I shared and more I typed");
	} finally {
		environment.close();
	}
});

it("reports a seed the store could not take while it is starting an earlier session", () => {
	const environment = opening();
	try {
		// A store mid-start ignores prompt edits; the seed must say so.
		environment.store.setState({ submitting: true });
		const seed = seedSharedPrompt(environment.store, environment.memory, { id: "s1", text: "What I shared" });
		expect(seed.saved).toBe(false);
		expect(shareSeedLanded(environment.store, seed.text)).toBe(false);
	} finally {
		environment.close();
	}
});
