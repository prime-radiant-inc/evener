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
	[true, null, null, "Connected"],
	[true, null, check(), "Connected · evener 0.9.412 · up to date"],
	[
		true,
		null,
		check({ updateAvailable: true, latestTag: "v0.9.413" }),
		"Connected · evener 0.9.412 · Update available",
	],
	[true, null, check({ applicable: false, buildChannel: "dev" }), "Connected · evener 0.9.412"],
	[false, "Reconnecting…", check(), "Reconnecting… · evener 0.9.412 · up to date"],
	[false, "Update needed", null, "Update needed"],
	// The shared line stays quiet through a blip's first 2 seconds; the
	// header still never claims a connection it doesn't have.
	[false, null, null, "Connecting…"],
	[false, null, check(), "Connecting… · evener 0.9.412 · up to date"],
] as const)("ready %s, connection %s, check %o → %s", (ready, connection, answer, expected) => {
	expect(hubStatusLine(ready, connection, answer)).toBe(expected);
});
