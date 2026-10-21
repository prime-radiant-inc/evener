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

	it("resumes a fenced session instead of asking for a Resume press", () => {
		// Send is the single recovery action on the phone too: a
		// recovery-fenced session takes the message, resumes, then sends it.
		expect(sendAction(session("idle", { resumeRequired: true }), [], true)).toBe("resume");
		expect(sendAction(session("notLoaded", { resumeRequired: true }), [], true)).toBe("resume");
	});

	it("resumes a fenced session in the shape the hub actually sends", () => {
		// applyThreadResumeRequirement clears the send capability as it sets
		// resumeRequired, so a fence read off the wire always looks like this.
		// Gating the resume on the capability would leave every real fenced
		// session with no action at all.
		const fencedWire = { capabilities: caps({ send: false }), resumeRequired: true };
		expect(sendAction(session("idle", fencedWire), [], true)).toBe("resume");
		expect(sendAction(session("notLoaded", fencedWire), [], true)).toBe("resume");
		expect(sendAction(session("active", fencedWire), [], true)).toBe("resume");
		expect(sendAction(session("idle", fencedWire), [], false)).toBe("resume");
		// A daemon that needs restarting still has no Send: its older process
		// must be stopped first.
		expect(sendAction(session("restartRequired", fencedWire), [], true)).toBe("none");
		// Only the first message resumes. A second one queues behind it, or two
		// parked turn/starts would race for the same active turn on recovery.
		expect(sendAction(session("idle", fencedWire), [pendingSend()], true)).toBe("queue");
		expect(sendAction(session("idle", fencedWire), [pendingSend()], false)).toBe("queue");
	});

	it("does nothing where the harness can't take it", () => {
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

describe("Send while offline (phase 6)", () => {
	it.each([
		["idle", "queue"],
		["awaiting", "queue"],
		["systemError", "queue"],
		["active", "queue"],
		["notLoaded", "resume"],
		["ended", "resume"],
		["closed", "resume"],
		["restartRequired", "none"],
	] as const)("%s → %s: it queues where a turn may be running by the time it arrives", (type, expected) => {
		expect(sendAction(session(type), [], false)).toBe(expected);
	});

	it("queues a message behind one this phone still holds, a shut-down session's first included", () => {
		expect(sendAction(session("ended"), [pendingSend()], false)).toBe("queue");
		expect(sendAction(session("notLoaded"), [pendingSend({ state: "blockedUnknown" })], false)).toBe("queue");
		expect(sendAction(session("idle"), [pendingSend()], false)).toBe("queue");
	});

	it("waits for the connection where the harness can't queue", () => {
		expect(sendAction(session("idle", { capabilities: caps({ queue: false }) }), [], false)).toBe("none");
		expect(sendAction(session("active", { capabilities: caps({ queue: false }) }), [], false)).toBe("none");
		// Offline, a fenced session's message parks in the outbox like any other
		// and the resume runs when the connection returns.
		expect(sendAction(session("idle", { resumeRequired: true }), [], false)).toBe("resume");
		expect(sendAction(session("ended", { capabilities: caps({ send: false }) }), [], false)).toBe("none");
	});

	it("says Send waits for the connection", () => {
		// Offline, Send says it waits, whatever it would do online; an answer
		// says so apart from a message.
		const offline = sendLabel("queue", false, false);
		expect(offline).toBe(sendLabel("send", false, false));
		expect(offline).not.toBe(sendLabel("queue", false));
		expect(offline).not.toBe(sendLabel("send", false));
		const answerOffline = sendLabel("queue", true, false);
		expect(answerOffline).not.toBe(offline);
		expect(answerOffline).not.toBe(sendLabel("queue", true));
	});
});
