import { expect, it } from "vitest";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { addLaunchMcp, resourceKey } from "./launchMcp";

it("uses the web whitespace syntax and validates the command on the hub", async () => {
	const client = new FakeClient("ready");
	client.on("evener/path/validate", () => ({ valid: true, path: "/usr/bin/env" }) as never);
	const original = [{ name: "existing", command: "/bin/old", args: ["one argument"] }];
	expect(await addLaunchMcp(client, original, " tools env  node server.js ")).toEqual([
		...original,
		{ name: "tools", command: "/usr/bin/env", args: ["node", "server.js"] },
	]);
	expect(client.calls.map((call) => ({ method: call.method, params: call.params }))).toEqual([
		{
			method: "evener/path/validate",
			params: { path: "env", kind: "command" },
		},
	]);
	expect(original).toHaveLength(1);
});
it("rejects incomplete syntax, invalid commands and transport failures", async () => {
	const client = new FakeClient("ready");
	client.on("evener/path/validate", () => ({ valid: false, error: "invalid fixture" }) as never);
	await expect(addLaunchMcp(client, [], "name")).rejects.toThrow();
	await expect(addLaunchMcp(client, [], "name missing-command")).rejects.toThrow();
	const offline = new FakeClient("ready");
	offline.on("evener/path/validate", () => {
		throw Error("offline");
	});
	await expect(addLaunchMcp(offline, [], "name command")).rejects.toThrow();
});
it("distinguishes MCP argument boundaries and resource kinds", () => {
	expect(resourceKey({ name: "n", command: "c", args: ["a b"] })).not.toBe(
		resourceKey({ name: "n", command: "c", args: ["a", "b"] }),
	);
	expect(resourceKey({ name: "n", command: "c" })).toBe(resourceKey({ name: "n", command: "c", args: [] }));
	expect(resourceKey("n")).not.toBe(resourceKey({ name: "n", command: "" }));
});
