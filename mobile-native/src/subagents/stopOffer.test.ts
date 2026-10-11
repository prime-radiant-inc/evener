import { describe, expect, it } from "vitest";
import { stopOffer } from "./stopOffer";

// What a running subagent's bar offers to stop it (spec 9, ruling 10, S6).
describe("the subagent bar's stop offer", () => {
	const running = { state: "running", active: true } as const;
	const failedWithWork = { state: "failed", active: true } as const;
	const idle = { state: "done", active: false } as const;
	it.each([
		["no row in the tree", { row: null, requested: false, direct: "yes" }, "none"],
		["a request already sent", { row: running, requested: true, direct: "yes" }, "requested"],
		["the coordinator not read yet", { row: running, requested: false, direct: "unknown" }, "none"],
		["a direct stop, its own run going", { row: running, requested: false, direct: "yes" }, "stop"],
		["a direct stop, its own run over", { row: failedWithWork, requested: false, direct: "yes" }, "none"],
		["no direct stop, work under it", { row: failedWithWork, requested: false, direct: "no" }, "ask"],
		["no direct stop, nothing running", { row: idle, requested: false, direct: "no" }, "none"],
	] as const)("%s: %s", (_name, input, expected) => {
		expect(stopOffer(input)).toBe(expected);
	});
});
