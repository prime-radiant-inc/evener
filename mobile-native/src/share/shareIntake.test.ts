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

it("leaves a dismissed share pending and offers it on the next wake", async () => {
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({
		port: sharePort([share("one", HOUR), share("two", 2 * HOUR)]),
		onShare: (entry) => offered.push(entry),
	});
	await intake.offer();
	intake.release();
	expect(intake.busy).toBe(false);
	// A dismissal does not reopen the sheet by itself.
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
	await intake.offer();
	expect(offered.map((entry) => entry.id)).toEqual(["one", "one"]);
});

it("offers the next share once the placed one is forgotten", async () => {
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({
		port: sharePort([share("one", HOUR), share("two", 2 * HOUR)]),
		onShare: (entry) => offered.push(entry),
	});
	await intake.offer();
	intake.forget("one");
	await intake.offer();
	expect(offered.map((entry) => entry.id)).toEqual(["one", "two"]);
	intake.release();
	await intake.offer();
	// Two is still pending (only the sheet closed), so it comes back; one was
	// placed and never does.
	expect(offered.map((entry) => entry.id)).toEqual(["one", "two", "two"]);
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

it("offers the next share after a placed one advances the intake", async () => {
	const offered: SharedPayload[] = [];
	const intake = createShareIntake({
		port: sharePort([share("one", HOUR), share("two", 2 * HOUR)]),
		onShare: (entry) => offered.push(entry),
	});
	await intake.offer();
	intake.forget("one");
	await intake.advance("one");
	expect(offered.map((entry) => entry.id)).toEqual(["one", "two"]);
});

it("keeps a placed share pending when its file cannot be deleted", async () => {
	const offered: SharedPayload[] = [];
	const subject = sharePort([share("one", HOUR)]);
	subject.remove = async () => {
		throw new Error("disk busy");
	};
	const intake = createShareIntake({ port: subject, onShare: (entry) => offered.push(entry) });
	await intake.offer();
	intake.forget("one");
	await intake.advance("one");
	expect(offered.map((entry) => entry.id)).toEqual(["one"]);
});
