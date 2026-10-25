// The native adapter's one new mapping: the wire resume fence is
// explicit-resume for the shared send/queue gate, and absent otherwise.
// sendAction.test.ts stays the behavior oracle; this file only pins the
// adapter seam.
import type { ThreadCapabilities } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { type SendSource, nativeSendQueueRecovery, sendAction } from "./sendAction";

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

const session = (over: Partial<SendSource> = {}): SendSource => ({
	status: { type: "idle" },
	capabilities: caps(),
	...over,
});

describe("native send recovery mapping", () => {
	it("maps resumeRequired to explicit-resume", () => {
		expect(nativeSendQueueRecovery(session({ resumeRequired: true }))).toEqual({ kind: "explicit-resume" });
	});

	it("maps an absent or false resumeRequired to none", () => {
		expect(nativeSendQueueRecovery(session())).toEqual({ kind: "none" });
		expect(nativeSendQueueRecovery(session({ resumeRequired: false }))).toEqual({ kind: "none" });
	});

	it("feeds that mapping into the shared gate", () => {
		expect(sendAction(session({ resumeRequired: true }), [], true)).toBe("resume");
		expect(sendAction(session(), [], true)).toBe("send");
	});
});
