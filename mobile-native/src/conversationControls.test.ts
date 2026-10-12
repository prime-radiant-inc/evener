import { expect, it } from "vitest";
import { canComposeFor, conversationControls, queueActionRefusal } from "./conversationControls";

const STEERING = {
	send: true,
	steer: true,
	interrupt: true,
	compact: true,
	clear: false,
	forkFromTurn: false,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	sharedNotes: false,
	queue: false,
	goal: true,
	rename: true,
};

function conversation(status: string, depth = 0, capabilities = STEERING) {
	return { status: { type: status }, capabilities, queue: { revision: 0, depth } };
}

// The hub advertises steer as harness support, so an idle steering harness
// carries steer:true; the Steer action follows the status.
it("offers no Steer action on an idle steering harness", () => {
	expect(conversationControls(conversation("idle")).steer).toBe(false);
	expect(conversationControls(conversation("active")).steer).toBe(true);
});

it("can compose when any action or a goal is available, not otherwise", () => {
	expect(canComposeFor(conversation("idle"))).toBe(true);
	expect(canComposeFor(conversation("active", 0, { ...STEERING, send: false, goal: false }))).toBe(true);
	expect(canComposeFor(conversation("active", 0, { ...STEERING, send: false, steer: false, goal: false }))).toBe(false);
});

// The sheet re-checks at press time: a status that flipped to awaiting between
// the render that offered the action and the press refuses with the status
// reason, and a harness without steer with the capability reason.
it("refuses a queue action pressed after the status flipped, with the control's reason", () => {
	for (const action of ["promote", "drainAll"] as const) {
		const statusReason = queueActionRefusal(conversation("awaiting", 1), action);
		const capabilityReason = queueActionRefusal(conversation("idle", 1, { ...STEERING, steer: false }), action);
		expect(queueActionRefusal(conversation("active", 1), action)).toBeNull();
		expect(statusReason).not.toBeNull();
		expect(capabilityReason).not.toBeNull();
		expect(capabilityReason).not.toBe(statusReason);
	}
});

// Drain-all sends the queue and nothing else, so with the queue emptied between
// the render and the press there is nothing to drain: refused at the boundary
// with the queue reason, not offered to the daemon to refuse.
it("refuses a drain-all on an emptied queue with the queue reason", () => {
	const emptied = queueActionRefusal(conversation("active", 0), "drainAll");
	expect(emptied).not.toBeNull();
	// The queue's own reason, not the status's.
	expect(emptied).not.toBe(queueActionRefusal(conversation("awaiting", 1), "drainAll"));
	expect(queueActionRefusal(conversation("active", 1), "drainAll")).toBeNull();
	// A promote names one row; its rule stays the drain's.
	expect(queueActionRefusal(conversation("active", 0), "promote")).toBeNull();
});
