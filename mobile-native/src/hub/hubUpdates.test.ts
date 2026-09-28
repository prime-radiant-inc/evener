import type { UpdateCheckResponse } from "@evener/appwire-client";
import { expect, it } from "vitest";
import { createPhoneHubUpdates, createReadiness } from "./hubUpdates";

const check = (over: Partial<UpdateCheckResponse> = {}): UpdateCheckResponse => ({
	channel: "release",
	buildChannel: "release",
	currentVersion: "0.9.412",
	currentCommit: "abc1234",
	latestTag: "v0.9.413",
	updateAvailable: true,
	applicable: true,
	...over,
});

/** A hub that answers every update check from `version` and every apply by
 * restarting. */
function hub() {
	const calls: string[] = [];
	const state = { version: "0.9.412" };
	const client = {
		request: (async (method: string) => {
			calls.push(method);
			if (method === "evener/update/apply") return { restarting: true };
			return check({ currentVersion: state.version, updateAvailable: state.version === "0.9.412" });
		}) as never,
	};
	return { client, calls, state };
}
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

it("checks, applies, and clears the restart once the hub is back on a new version", async () => {
	const h = hub();
	const readiness = createReadiness();
	readiness.set(true);
	const updates = createPhoneHubUpdates(h.client, readiness);
	await updates.controller.runCheck();
	const applying = updates.controller.apply();
	await settle();
	expect(updates.controller.getState().restarting).toBe(true);
	// The hub drops the connection while it restarts, then comes back.
	readiness.set(false);
	readiness.set(true);
	await settle();
	// Back on the old version: still restarting.
	expect(updates.controller.getState().restarting).toBe(true);
	h.state.version = "0.9.413";
	readiness.set(false);
	readiness.set(true);
	await applying;
	expect(updates.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: false });
});

it("stops waiting when the sheet lets go of it", async () => {
	const h = hub();
	const readiness = createReadiness();
	readiness.set(true);
	const updates = createPhoneHubUpdates(h.client, readiness);
	await updates.controller.runCheck();
	const applying = updates.controller.apply();
	await settle();
	updates.dispose();
	await applying;
	h.state.version = "0.9.413";
	readiness.set(false);
	readiness.set(true);
	await settle();
	expect(h.calls.filter((method) => method === "evener/update/check")).toHaveLength(1);
});

it("reports an update that can't be sent without a client", async () => {
	const readiness = createReadiness();
	const updates = createPhoneHubUpdates(null, readiness);
	await updates.controller.runCheck();
	expect(updates.controller.getState().checkError).toBeTruthy();
});

it("tells listeners only when readiness turns true", () => {
	const readiness = createReadiness();
	let heard = 0;
	readiness.subscribe(() => {
		heard += 1;
	});
	readiness.set(false);
	readiness.set(true);
	readiness.set(true);
	readiness.set(false);
	readiness.set(true);
	expect(heard).toBe(2);
	expect(readiness.isReady()).toBe(true);
});
