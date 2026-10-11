import { describe, expect, it } from "vitest";
import type { HostRow } from "@evener/appwire-client";
import { hostStatus, systemLabel, versionDriftTag } from "./hostStatus";

const row = (over: Partial<HostRow> = {}): HostRow => ({
	name: "paradise-park",
	origin: "hub.toml",
	attached: false,
	midAttach: false,
	removed: false,
	generation: 1,
	incarnationId: "incarnation-1",
	...over,
});

describe("a host's state (ruling 4)", () => {
	it.each([
		[{ attached: true }, false, "Connected", "ink", false],
		[{}, true, "Connecting…", "ink", false],
		[{ midAttach: true }, false, "Offline · reconnecting", "attention", false],
		[{}, false, "Offline", "attention", true],
		[
			{ lastAttachError: "ssh: connect to host paradise-park port 22: Connection refused" },
			false,
			"Offline",
			"attention",
			true,
		],
		[{ removed: true }, false, "Offline", "attention", false],
	] as const)("%o, connecting %s → %s", (over, connecting, word, tone, canConnect) => {
		expect(hostStatus(row(over), connecting)).toMatchObject({ word, tone, canConnect });
	});

	it("says what an offline host's footer says (spec 12)", () => {
		expect(hostStatus(row({ midAttach: true }), false).footer).toBe(
			"This host is offline, so its sessions can't be reached. The hub keeps trying to reach it.",
		);
		expect(hostStatus(row(), false).footer).toBe(
			"This host is offline, so its sessions can't be reached. Connect to reach them.",
		);
		expect(hostStatus(row({ attached: true }), false).footer).toBeNull();
	});
});

describe("version drift and the system line", () => {
	it("tags a host on another version, from its last-known version too", () => {
		expect(versionDriftTag(row({ attached: true, hubVersion: "0.9.409" }), "0.9.412")).toBe("Hub runs 0.9.412");
		expect(versionDriftTag(row({ hubVersion: "0.9.409" }), "0.9.412")).toBe("Hub runs 0.9.412");
		expect(versionDriftTag(row({ attached: true, hubVersion: "0.9.412" }), "0.9.412")).toBeNull();
		expect(versionDriftTag(row({ attached: true }), "0.9.412")).toBeNull();
		expect(versionDriftTag(row({ hubVersion: "0.9.409" }), undefined)).toBeNull();
	});

	it("names the system in words", () => {
		expect(systemLabel({ os: "darwin", arch: "arm64" })).toBe("macOS · arm64");
		expect(systemLabel({ os: "linux", arch: "amd64" })).toBe("Linux · amd64");
		expect(systemLabel({ os: "freebsd" })).toBe("freebsd");
		expect(systemLabel({})).toBeNull();
	});
});
