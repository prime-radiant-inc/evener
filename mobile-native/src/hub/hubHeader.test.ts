import { expect, it } from "vitest";
import type { UpdateCheckResponse } from "@evener/appwire-client";
import { hubStatusLine } from "./hubHeader";

const check = (over: Partial<UpdateCheckResponse> = {}): UpdateCheckResponse => ({
	channel: "release",
	buildChannel: "release",
	currentVersion: "0.9.412",
	currentCommit: "abc1234",
	updateAvailable: false,
	applicable: true,
	...over,
});

it.each([
	[null, null, "Connected"],
	[null, check(), "Connected · evener 0.9.412 · up to date"],
	[null, check({ updateAvailable: true, latestTag: "v0.9.413" }), "Connected · evener 0.9.412 · Update available"],
	[null, check({ applicable: false, buildChannel: "dev" }), "Connected · evener 0.9.412"],
	["Reconnecting…", check(), "Reconnecting… · evener 0.9.412 · up to date"],
	["Update needed", null, "Update needed"],
] as const)("connection %s, check %o → %s", (connection, answer, expected) => {
	expect(hubStatusLine(connection, answer)).toBe(expected);
});
