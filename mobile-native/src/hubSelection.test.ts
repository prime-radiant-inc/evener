import { describe, expect, it } from "vitest";
import { type HubProfile, HubProfiles, type SecureStorage } from "./connection";
import { HubSelection } from "./hubSelection";

class DeferredStorage implements SecureStorage {
	private data = new Map<string, string>();
	private gates = new Map<string, Promise<void>>();
	private release = new Map<string, () => void>();
	private enteredResolvers = new Map<string, () => void>();
	private entered = new Map<string, Promise<void>>();
	private reading: {
		key: string;
		skip: number;
		entered(): void;
		release: Promise<void>;
	} | null = null;
	failRead = false;
	failWrite = false;
	failDelete = false;
	holdRead(key: string, skip = 0) {
		const entered = Promise.withResolvers<void>();
		const release = Promise.withResolvers<void>();
		this.reading = {
			key,
			skip,
			entered: entered.resolve,
			release: release.promise,
		};
		return { entered: entered.promise, release: release.resolve };
	}
	block(key: string) {
		this.gates.set(
			key,
			new Promise<void>((resolve) => this.release.set(key, resolve)),
		);
		this.entered.set(
			key,
			new Promise<void>((resolve) => this.enteredResolvers.set(key, resolve)),
		);
	}
	waitUntilEntered(key: string) {
		const entered = this.entered.get(key);
		if (!entered) throw new Error("No blocked storage write");
		return entered;
	}
	unblock(key: string) {
		this.release.get(key)?.();
		this.release.delete(key);
	}
	async getItemAsync(key: string) {
		if (this.failRead) throw new Error("Storage read failed");
		const value = this.data.get(key) ?? null;
		const reading = this.reading;
		if (reading?.key === key && reading.skip-- === 0) {
			this.reading = null;
			reading.entered();
			await reading.release;
		}
		return value;
	}
	async setItemAsync(key: string, value: string) {
		if (this.failWrite) throw new Error("Storage write failed");
		this.enteredResolvers.get(key)?.();
		this.enteredResolvers.delete(key);
		await this.gates.get(key);
		this.data.set(key, value);
	}
	async deleteItemAsync(key: string) {
		if (this.failDelete) throw new Error("Storage delete failed");
		this.data.delete(key);
	}
}

function harness() {
	const storage = new DeferredStorage();
	const profiles = new HubProfiles(storage);
	let selected: string | null = null;
	let retryCount = 0;
	let roster: HubProfile[] = [];
	let nextId = 0;
	const selection = new HubSelection(
		profiles,
		{
			onProfiles: (value) => {
				roster = value;
			},
			onSelect: (id) => {
				selected = id;
			},
			onRetry: () => {
				retryCount += 1;
			},
		},
		() => `saved-${++nextId}`,
	);
	return {
		storage,
		profiles,
		selection,
		get selected() {
			return selected;
		},
		get retryCount() {
			return retryCount;
		},
		get roster() {
			return roster;
		},
	};
}

describe("HubSelection", () => {
	it("does not replace a saved hub with a delayed initial roster", async () => {
		const h = harness();
		const read = h.storage.holdRead("evener.hubs");
		const loading = h.selection.load();
		await read.entered;
		await h.selection.save({ name: "A", origin: "https://a.test", token: "a" });
		read.release();
		await loading;
		expect(h.roster.map((p) => p.id)).toEqual(["saved-1"]);
		expect(h.selected).toBe("saved-1");
	});

	it("retries committed credentials even when their roster refresh fails", async () => {
		const h = harness();
		await h.selection.save({
			name: "A",
			origin: "https://a.test",
			token: "old",
		});
		const read = h.storage.holdRead("evener.hubs", 1);
		const updating = h.selection.update("saved-1", { name: "A", token: "new" });
		const failed = expect(updating).rejects.toThrow();
		await read.entered;
		h.storage.failRead = true;
		read.release();
		await failed;
		h.storage.failRead = false;
		expect(await h.profiles.token("saved-1")).toBe("new");
		expect(h.retryCount).toBe(1);
	});

	it("switches to a normally saved hub while another hub is selected", async () => {
		const h = harness();
		await h.selection.save({ name: "A", origin: "https://a.test", token: "a" });
		expect(
			await h.selection.save({
				name: "B",
				origin: "https://b.test",
				token: "b",
			}),
		).toBe(true);
		expect(h.selected).toBe("saved-2");
		expect(h.roster.map((p) => p.id)).toEqual(["saved-1", "saved-2"]);
	});

	it("does not restore an old location over a selection or pending save", async () => {
		const h = harness();
		h.selection.restore("restored");
		expect(h.selected).toBe("restored");
		h.selection.select("chosen");
		h.selection.restore("restored");
		expect(h.selected).toBe("chosen");
		h.selection.disconnect();
		h.selection.restore("restored");
		expect(h.selected).toBeNull();
		const fresh = harness();
		const read = fresh.storage.holdRead("evener.hub.saved-1");
		const saving = fresh.selection.save({
			name: "A",
			origin: "https://a.test",
			token: "a",
		});
		await read.entered;
		fresh.selection.restore("restored");
		expect(fresh.selected).toBeNull();
		read.release();
		expect(await saving).toBe(true);
	});

	it("retries the selected hub only for a completed token change", async () => {
		const h = harness();
		await h.selection.save({
			name: "A",
			origin: "https://a.test",
			token: "old",
		});
		await h.selection.update("saved-1", { name: "Renamed" });
		expect(h.retryCount).toBe(0);
		h.storage.block("evener.hub.saved-1");
		const updating = h.selection.update("saved-1", {
			name: "Renamed",
			token: "new",
		});
		await h.storage.waitUntilEntered("evener.hub.saved-1");
		expect(h.retryCount).toBe(0);
		h.storage.unblock("evener.hub.saved-1");
		await updating;
		expect(h.retryCount).toBe(1);
		expect(await h.profiles.token("saved-1")).toBe("new");
	});

	it("does not resurrect a removed hub from a delayed save roster", async () => {
		const h = harness();
		const read = h.storage.holdRead("evener.hub.saved-1");
		const saving = h.selection.save({
			name: "A",
			origin: "https://a.test",
			token: "a",
		});
		await read.entered;
		const cleaned: string[] = [];
		await h.selection.remove("saved-1", {
			removeHub: (id) => {
				cleaned.push(id);
			},
		});
		expect(h.roster).toEqual([]);
		read.release();
		const selected = await saving;
		expect(h.roster).toEqual([]);
		expect(selected).toBe(false);
		expect(h.selected).toBeNull();
		expect(cleaned).toEqual(["saved-1"]);
	});

	it("does not discard a new hub when an older update roster arrives", async () => {
		const h = harness();
		await h.selection.save({ name: "A", origin: "https://a.test", token: "a" });
		const read = h.storage.holdRead("evener.hub.saved-1", 1);
		const updating = h.selection.update("saved-1", { name: "Renamed" });
		await read.entered;
		await h.selection.save({ name: "B", origin: "https://b.test", token: "b" });
		read.release();
		await updating;
		expect(h.roster.map((p) => p.id)).toEqual(["saved-1", "saved-2"]);
		expect(h.roster[0]?.name).toBe("Renamed");
		expect(h.selected).toBe("saved-2");
	});

	it.each(["write", "delete", "read"] as const)(
		"reconciles removal when storage %s fails",
		async (failure) => {
			const h = harness();
			await h.selection.save({
				name: "A",
				origin: "https://a.test",
				token: "a",
			});
			if (failure === "write") h.storage.failWrite = true;
			if (failure === "delete") h.storage.failDelete = true;
			if (failure === "read") h.storage.failRead = true;
			const cleaned: string[] = [];
			await expect(
				h.selection.remove("saved-1", {
					removeHub: (id) => {
						cleaned.push(id);
					},
				}),
			).rejects.toThrow();
			const removed = failure === "delete";
			expect(h.selected).toBe(removed ? null : "saved-1");
			expect(h.roster.map((p) => p.id)).toEqual(removed ? [] : ["saved-1"]);
			expect(cleaned).toEqual(removed ? ["saved-1"] : []);
		},
	);

	it("drops a removed row even when the follow-up roster cannot be read", async () => {
		const h = harness();
		await h.selection.save({ name: "A", origin: "https://a.test", token: "a" });
		const read = h.storage.holdRead("evener.hubs");
		const removing = h.selection.remove("saved-1", {
			removeHub: () => undefined,
		});
		const failed = expect(removing).rejects.toThrow();
		await read.entered;
		h.storage.failRead = true;
		read.release();
		await failed;
		expect(h.roster).toEqual([]);
		expect(h.selected).toBeNull();
	});

	it("selects a normally saved hub and publishes the refreshed roster", async () => {
		const h = harness();
		expect(
			await h.selection.save({
				name: "A",
				origin: "https://a.test",
				token: "token",
			}),
		).toBe(true);
		expect(h.selected).toBe("saved-1");
		expect(h.roster).toHaveLength(1);
	});

	it("does not select an older save after a newer selection intent", async () => {
		const h = harness();
		h.storage.block("evener.hub.saved-1");
		const saving = h.selection.save({
			name: "A",
			origin: "https://a.test",
			token: "token",
		});
		await h.storage.waitUntilEntered("evener.hub.saved-1");
		h.selection.select("newer");
		h.storage.unblock("evener.hub.saved-1");
		expect(await saving).toBe(false);
		expect(h.selected).toBe("newer");
	});

	it.each([false, true])(
		"retries updated credentials only on the current hub (switch back: %s)",
		async (switchBack) => {
			const h = harness();
			await h.selection.save({
				name: "A",
				origin: "https://a.test",
				token: "old",
			});
			h.storage.block("evener.hub.saved-1");
			const updating = h.selection.update("saved-1", {
				name: "A",
				token: "new",
			});
			await h.storage.waitUntilEntered("evener.hub.saved-1");
			h.selection.select("b");
			if (switchBack) h.selection.select("saved-1");
			h.storage.unblock("evener.hub.saved-1");
			await updating;
			expect(h.retryCount).toBe(switchBack ? 3 : 1);
		},
	);

	it("does not select a save after disconnecting while it is persisted", async () => {
		const h = harness();
		h.storage.block("evener.hub.saved-1");
		const saving = h.selection.save({
			name: "A",
			origin: "https://a.test",
			token: "token",
		});
		await h.storage.waitUntilEntered("evener.hub.saved-1");
		h.selection.disconnect();
		h.storage.unblock("evener.hub.saved-1");
		expect(await saving).toBe(false);
		expect(h.selected).toBeNull();
	});

	it("lets the newest save own selection when saves overlap", async () => {
		const h = harness();
		h.storage.block("evener.hub.saved-1");
		const first = h.selection.save({
			name: "A",
			origin: "https://a.test",
			token: "a",
		});
		await h.storage.waitUntilEntered("evener.hub.saved-1");
		const second = h.selection.save({
			name: "B",
			origin: "https://b.test",
			token: "b",
		});
		h.storage.unblock("evener.hub.saved-1");
		expect(await first).toBe(false);
		expect(await second).toBe(true);
		expect(h.selected).toBe("saved-2");
	});

	it("does not republish a current hub after a queued save then removal", async () => {
		const h = harness();
		await h.selection.save({ name: "A", origin: "https://a.test", token: "a" });
		h.storage.block("evener.hub.saved-2");
		const saving = h.selection.save({
			name: "B",
			origin: "https://b.test",
			token: "b",
		});
		await h.storage.waitUntilEntered("evener.hub.saved-2");
		const removing = h.selection.remove("saved-1", {
			removeHub: () => undefined,
		});
		h.storage.unblock("evener.hub.saved-2");
		expect(await saving).toBe(false);
		await removing;
		expect(h.roster.map((profile) => profile.id)).toEqual(["saved-2"]);
		expect(h.selected).toBeNull();
	});
});
