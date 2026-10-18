import { expect, it } from "vitest";
import { createShareIntake } from "./shareIntake";
import type { SharedPayload } from "./sharePayload";
import { HOUR, share, sharePort } from "./shareTestkit";

it("offers the oldest pending share once", async () => {
	const older = share("older", HOUR);
	const newer = share("newer", 2 * HOUR);
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({ port: sharePort([newer, older]), onShare: (entry) => offered.push(entry) });
	await intake.offer();
	expect(offered.map((entry) => entry.id)).toEqual(["older"]);
	expect(intake.busy).toBe(true);
	await intake.offer();
	expect(offered.map((entry) => entry.id)).toEqual(["older"]);
});

it("offers the next share once the sheet releases", async () => {
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({
		port: sharePort([share("one", HOUR), share("two", 2 * HOUR)]),
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
		port: sharePort([share("one", HOUR), share("two", 2 * HOUR)]),
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
		},
		onShare: (entry) => offered.push(entry),
	});
	await intake.offer();
	expect(offered).toEqual([]);
	expect(intake.busy).toBe(false);
});

it("offers one share when two wakes land at once", async () => {
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({
		port: sharePort([share("one", HOUR), share("two", 2 * HOUR)]),
		onShare: (entry) => offered.push(entry),
	});
	await Promise.all([intake.offer(), intake.offer()]);
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
});
