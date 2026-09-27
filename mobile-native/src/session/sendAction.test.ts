import type { ThreadCapabilities } from "@evener/appwire-client";
import type { PendingTurnEntry } from "@evener/appwire-client/state/mutation";
import { describe, expect, it } from "vitest";
import { composerPlaceholder, type SendSource, sendAction, sendLabel } from "./sendAction";

const caps = (over: Partial<ThreadCapabilities> = {}): ThreadCapabilities => ({
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	queue: true,
	goal: true,
	sharedNotes: true,
	rename: true,
	...over,
});
const session = (type: string, over: Partial<SendSource> = {}): SendSource => ({
	status: { type },
	capabilities: caps(),
	...over,
});
const pendingSend = (over: Partial<PendingTurnEntry> = {}): PendingTurnEntry => ({
	id: "cmid-1",
	ref: "ref-1",
	method: "send",
	text: "first",
	imageCount: 0,
	skillNames: [],
	state: "submitting",
	source: "outbox",
	fromThisClient: true,
	...over,
});

describe("what Send does (spec 8.5)", () => {
	it.each([
		["idle", "send"],
		["awaiting", "send"],
		["systemError", "send"],
		["active", "queue"],
		["notLoaded", "resume"],
		["ended", "resume"],
		["closed", "resume"],
		["restartRequired", "none"],
	] as const)("%s → %s", (type, expected) => {
		expect(sendAction(session(type), [], true)).toBe(expected);
	});

	it("queues a second message while this client's own send is unreflected", () => {
		expect(sendAction(session("idle"), [pendingSend()], true)).toBe("queue");
		expect(sendAction(session("idle"), [pendingSend({ state: "blockedUnknown" })], true)).toBe("queue");
		expect(sendAction(session("systemError"), [pendingSend()], true)).toBe("queue");
	});

	it("ignores a send that Stop canceled and another client's send", () => {
		expect(sendAction(session("idle"), [pendingSend({ state: "canceled" })], true)).toBe("send");
		expect(sendAction(session("idle"), [pendingSend({ fromThisClient: false })], true)).toBe("send");
	});

	it("does nothing offline, while paused, or where the harness can't take it", () => {
		expect(sendAction(session("idle"), [], false)).toBe("none");
		expect(sendAction(session("idle", { resumeRequired: true }), [], true)).toBe("none");
		expect(sendAction(session("active", { capabilities: caps({ queue: false }) }), [], true)).toBe("none");
		expect(sendAction(session("ended", { capabilities: caps({ send: false }) }), [], true)).toBe("none");
		expect(sendAction(session("idle", { capabilities: caps({ send: false }) }), [], true)).toBe("none");
	});
});

describe("the composer says what Send will do", () => {
	it.each([
		["send", false, "Message", "Send"],
		["queue", false, "Tell the agent something…", "Queue message"],
		["resume", false, "Message to resume", "Send and resume"],
		["none", false, "Message", "Send"],
		["send", true, "Answer or ask…", "Send answer"],
	] as const)("%s, question pending %s", (action, question, placeholder, label) => {
		expect(composerPlaceholder(action, question)).toBe(placeholder);
		expect(sendLabel(action, question)).toBe(label);
	});
});
