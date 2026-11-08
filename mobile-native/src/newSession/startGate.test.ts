import { describe, expect, it } from "vitest";
import type { HostRow } from "@evener/appwire-client";
import { hostRow } from "../hosts/hostsTestUtils";
import { hostReach, type StartInput, startBlock } from "./startGate";

const ready: StartInput = {
	ready: true,
	busy: false,
	cwd: "/home/jesse/git/evener",
	host: "local",
	hostLabel: "magic-kingdom",
	reach: "local",
	pluginIssues: [],
	unconfirmedModel: null,
	startMayRepeat: false,
	memoryOff: false,
	hostOffersMemory: null,
};
const row = (over: Partial<HostRow>): HostRow => hostRow("paradise-park", over);

describe("where a host stands", () => {
	it("is local for the hub's own machine, and pending before the hub lists its hosts", () => {
		expect(hostReach("local", null)).toBe("local");
		expect(hostReach("paradise-park", null)).toBe("pending");
	});

	it("reads the hub's list for any other host", () => {
		expect(hostReach("paradise-park", [row({})])).toBe("connected");
		expect(hostReach("paradise-park", [row({ attached: false })])).toBe("offline");
		expect(hostReach("paradise-park", [row({ attached: false, midAttach: true })])).toBe("offline");
		expect(hostReach("paradise-park", [])).toBe("missing");
	});
});

describe("what keeps Start disabled", () => {
	it("is nothing when the form can start", () => {
		expect(startBlock(ready)).toBeNull();
		expect(startBlock({ ...ready, host: "paradise-park", hostLabel: "paradise-park", reach: "connected" })).toBeNull();
		expect(startBlock({ ...ready, host: "paradise-park", hostLabel: "paradise-park", reach: "pending" })).toBeNull();
	});

	it("waits quietly for the connection and for work in flight", () => {
		expect(startBlock({ ...ready, ready: false })).toEqual({ field: null, message: null });
		expect(startBlock({ ...ready, busy: true })).toEqual({ field: null, message: null });
	});

	it("never starts on an offline or missing host (Review Focus 3)", () => {
		expect(startBlock({ ...ready, host: "paradise-park", hostLabel: "paradise-park", reach: "offline" })).toEqual({
			field: "host",
			message: "paradise-park is offline. Connect it or choose another host.",
		});
		expect(startBlock({ ...ready, host: "paradise-park", hostLabel: "paradise-park", reach: "missing" })).toEqual({
			field: "host",
			message: "paradise-park is no longer a host on this hub. Choose another host.",
		});
	});

	it("asks for a project, quietly", () => {
		expect(startBlock({ ...ready, cwd: "  " })).toEqual({ field: "project", message: null });
	});

	it("names each plugin with a blocking problem", () => {
		expect(
			startBlock({
				...ready,
				pluginIssues: [
					{ name: "superpowers-chrome", reason: "needs Chrome on this host" },
					{ name: "gone", reason: "not present in current preview" },
				],
			}),
		).toEqual({
			field: "plugins",
			message: "superpowers-chrome: needs Chrome on this host\ngone: not present in current preview",
		});
	});
});

describe("a chosen model the host couldn't list", () => {
	it("blames Model and says how to start anyway", () => {
		expect(startBlock({ ...ready, unconfirmedModel: "GLM 5.3 Vision" })).toEqual({
			field: "model",
			message: "Couldn't load this host's models, so GLM 5.3 Vision can't be used. Choose Hub default to start.",
		});
	});

	it("comes after the host and the project", () => {
		expect(startBlock({ ...ready, cwd: "", unconfirmedModel: "GLM 5.3 Vision" })).toEqual({
			field: "project",
			message: null,
		});
	});
});

describe("a Memory choice the chosen host can't honor", () => {
	it("holds Start over a kept Memory off, saying where to clear it", () => {
		expect(startBlock({ ...ready, memoryOff: true, hostOffersMemory: false })).toEqual({
			field: "moreOptions",
			message: "magic-kingdom can't turn memory off for a session. Clear it in More options or choose another host.",
		});
	});

	it("lets a Memory off through once the host offers the field", () => {
		expect(startBlock({ ...ready, memoryOff: true, hostOffersMemory: true })).toBeNull();
	});

	it("waits for the schema without holding Start: the hub checks the host at Start", () => {
		expect(startBlock({ ...ready, memoryOff: true, hostOffersMemory: null })).toBeNull();
	});

	it("never holds Start over a Memory on, which an older hub already runs anyway", () => {
		expect(startBlock({ ...ready, memoryOff: false, hostOffersMemory: false })).toBeNull();
	});

	it("comes after the project and the plugins", () => {
		expect(startBlock({ ...ready, cwd: "", memoryOff: true, hostOffersMemory: false })).toEqual({
			field: "project",
			message: null,
		});
		expect(
			startBlock({
				...ready,
				memoryOff: true,
				hostOffersMemory: false,
				pluginIssues: [{ name: "gone", reason: "not present in current preview" }],
			}),
		).toEqual({ field: "plugins", message: "gone: not present in current preview" });
	});
});

describe("a draft whose last start may have worked (#3104)", () => {
	it("holds Start, leaving the store's own error as the one line that says why", () => {
		expect(startBlock({ ...ready, startMayRepeat: true })).toEqual({ field: null, message: null });
	});
});
