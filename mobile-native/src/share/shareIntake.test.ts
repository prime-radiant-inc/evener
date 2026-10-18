import { expect, it } from "vitest";
import { createShareIntake } from "./shareIntake";
import { type SharePort, type SharedPayload, shareFileName } from "./sharePayload";

const HOUR = 60 * 60 * 1000;

function share(id: string, at: number): SharedPayload {
	return { id, text: `text ${id}`, createdAt: new Date(at).toISOString() };
}

/** A directory of share files that answers immediately, as expo-file-system's
 * app group reads do. */
function port(shares: SharedPayload[]): SharePort {
	const files = new Map(shares.map((entry) => [shareFileName(entry.id), entry]));
	return {
		async list() {
			return [...files.keys()];
		},
		async read(name) {
			const entry = files.get(name);
			if (!entry) throw new Error("gone");
			return JSON.stringify(entry);
		},
		async remove(name) {
			files.delete(name);
		},
		now: () => 0,
	};
}

it("offers the oldest pending share once", async () => {
	const older = share("older", HOUR);
	const newer = share("newer", 2 * HOUR);
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({ port: port([newer, older]), onShare: (entry) => offered.push(entry) });
	await intake.offer();
	expect(offered.map((entry) => entry.id)).toEqual(["older"]);
	expect(intake.busy).toBe(true);
	await intake.offer();
	expect(offered.map((entry) => entry.id)).toEqual(["older"]);
});

it("offers the next share once the sheet releases", async () => {
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({
		port: port([share("one", HOUR), share("two", 2 * HOUR)]),
		onShare: (entry) => offered.push(entry),
	});
	await intake.offer();
	await intake.release();
	expect(offered.map((entry) => entry.id)).toEqual(["one", "two"]);
	expect(intake.busy).toBe(true);
});

it("never offers a share again after it was forgotten", async () => {
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({
		port: port([share("one", HOUR), share("two", 2 * HOUR)]),
		onShare: (entry) => offered.push(entry),
	});
	await intake.offer();
	intake.forget("one");
	await intake.release();
	expect(offered.map((entry) => entry.id)).toEqual(["one", "two"]);
	await intake.release();
	expect(offered.map((entry) => entry.id)).toEqual(["one", "two"]);
});

it("offers nothing when the directory cannot be read", async () => {
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({
		port: {
			async list() {
				throw new Error("no container");
			},
			async read() {
				throw new Error("no container");
			},
			async remove() {},
			now: () => 0,
		},
		onShare: (entry) => offered.push(entry),
	});
	await intake.offer();
	expect(offered).toEqual([]);
	expect(intake.busy).toBe(false);
});
