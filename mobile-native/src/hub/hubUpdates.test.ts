import type { UpdateCheckResponse } from "@evener/appwire-client";
import { expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import {
	createPhoneHubUpdates,
	createReadiness,
	RESTART_DEADLINE_MS,
	RESTART_RECHECK_MS,
	useHubUpdates,
} from "./hubUpdates";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

const FIRST_START = "2026-10-21T12:00:00Z";
const SECOND_START = "2026-10-21T12:00:01Z";

const check = (over: Partial<UpdateCheckResponse> = {}): UpdateCheckResponse => ({
	channel: "release",
	buildChannel: "release",
	currentVersion: "0.9.412",
	currentCommit: "abc1234",
	latestTag: "v0.9.413",
	latestCommit: "def5678ffffffff",
	updateAvailable: true,
	applicable: true,
	onDiskCommit: "abc1234",
	installAvailable: true,
	restartPending: false,
	startedAt: FIRST_START,
	restartSupported: true,
	installTargetMatchesLaunch: true,
	...over,
});

/** A hub that answers update checks from its state, installs on upgrade, and
 * restarts on restart. */
function hub() {
	const calls: string[] = [];
	const state = { version: "0.9.412", startedAt: FIRST_START, installed: false, failNextCheck: false };
	const client = {
		request: (async (method: string) => {
			calls.push(method);
			if (method === "evener/upgrade") {
				state.installed = true;
				return {};
			}
			if (method === "evener/hub/restart") return { restarting: true };
			if (state.failNextCheck) {
				state.failNextCheck = false;
				throw new Error("the hub is still starting");
			}
			return check({
				currentVersion: state.version,
				startedAt: state.startedAt,
				updateAvailable: !state.installed,
				installAvailable: !state.installed,
				restartPending: state.installed,
				onDiskCommit: state.installed ? "def5678" : "abc1234",
			});
		}) as never,
	};
	return { client, calls, state };
}
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

it("checks, installs, and re-checks without restarting the hub", async () => {
	const h = hub();
	const readiness = createReadiness();
	readiness.set(true);
	const updates = createPhoneHubUpdates(h.client, readiness);
	await updates.controller.runCheck();
	await updates.controller.install();
	expect(h.calls).toEqual(["evener/update/check", "evener/upgrade", "evener/update/check"]);
	expect(updates.controller.getState()).toMatchObject({
		installing: false,
		installError: null,
		check: { installAvailable: false, restartPending: true },
	});
});

it("clears the restart once the hub is back on a new incarnation, even at the same version", async () => {
	const h = hub();
	const readiness = createReadiness();
	readiness.set(true);
	const updates = createPhoneHubUpdates(h.client, readiness);
	await updates.controller.runCheck();
	const restarting = updates.controller.restart();
	await settle();
	expect(updates.controller.getState().restarting).toBe(true);
	// The hub drops the connection while it restarts, then comes back on the
	// same build in a new process.
	h.state.startedAt = SECOND_START;
	readiness.set(false);
	readiness.set(true);
	await restarting;
	expect(updates.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: false });
});

it("keeps observing when a reconnect reaches the old process, then clears on the new one", async () => {
	const h = hub();
	const readiness = createReadiness();
	readiness.set(true);
	const updates = createPhoneHubUpdates(h.client, readiness);
	await updates.controller.runCheck();
	const restarting = updates.controller.restart();
	await settle();
	readiness.set(false);
	readiness.set(true);
	await new Promise((resolve) => setTimeout(resolve, 50));
	expect(updates.controller.getState()).toMatchObject({ restarting: true, restartTimedOut: false });

	h.state.startedAt = SECOND_START;
	await new Promise((resolve) => setTimeout(resolve, RESTART_RECHECK_MS + 50));
	await restarting;
	expect(updates.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: false });
});

it("retries a failed check in the restart window and clears on the new incarnation", async () => {
	const h = hub();
	const readiness = createReadiness();
	readiness.set(true);
	const updates = createPhoneHubUpdates(h.client, readiness);
	await updates.controller.runCheck();
	const restarting = updates.controller.restart();
	await settle();
	h.state.failNextCheck = true;
	readiness.set(false);
	readiness.set(true);
	await new Promise((resolve) => setTimeout(resolve, 50));
	expect(updates.controller.getState()).toMatchObject({ restarting: true, restartTimedOut: false });

	h.state.startedAt = SECOND_START;
	await new Promise((resolve) => setTimeout(resolve, RESTART_RECHECK_MS + 50));
	await restarting;
	expect(updates.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: false });
});

it("times out when the hub never reconnects", async () => {
	vi.useFakeTimers();
	try {
		const h = hub();
		const readiness = createReadiness();
		readiness.set(true);
		const updates = createPhoneHubUpdates(h.client, readiness);
		await updates.controller.runCheck();
		const restarting = updates.controller.restart();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		vi.advanceTimersByTime(RESTART_DEADLINE_MS);
		await restarting;
		expect(updates.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: true });
	} finally {
		vi.useRealTimers();
	}
});

it("stops waiting when the sheet lets go of it", async () => {
	const h = hub();
	const readiness = createReadiness();
	readiness.set(true);
	const updates = createPhoneHubUpdates(h.client, readiness);
	await updates.controller.runCheck();
	const restarting = updates.controller.restart();
	await settle();
	updates.dispose();
	await restarting;
	h.state.startedAt = SECOND_START;
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

it("checks only once the sheet has both a ready connection and its client", async () => {
	const h = hub();
	const current = { client: null as typeof h.client | null, ready: true };
	const hook = renderHook(() => useHubUpdates(current.client, current.ready));
	await settle();
	expect(h.calls).toEqual([]);
	expect(hook.result.current.getState().checkError).toBeNull();
	current.client = h.client;
	hook.rerender();
	await settle();
	expect(h.calls).toEqual(["evener/update/check"]);
	expect(hook.result.current.getState().check?.currentVersion).toBe("0.9.412");
	hook.unmount();
});

it("checks again after the connection comes back", async () => {
	const h = hub();
	const current = { client: h.client, ready: true };
	const hook = renderHook(() => useHubUpdates(current.client, current.ready));
	await settle();
	current.ready = false;
	hook.rerender();
	current.ready = true;
	hook.rerender();
	await settle();
	expect(h.calls).toEqual(["evener/update/check", "evener/update/check"]);
	hook.unmount();
});

it("lets go of a replaced connection's controller and checks through the new client", async () => {
	const first = hub();
	const second = hub();
	second.state.version = "0.9.413";
	const current = { client: first.client, ready: true };
	const hook = renderHook(() => useHubUpdates(current.client, current.ready));
	await settle();
	const replaced = hook.result.current;
	current.client = second.client;
	hook.rerender();
	await settle();
	expect(hook.result.current).not.toBe(replaced);
	expect(first.calls).toEqual(["evener/update/check"]);
	expect(second.calls).toEqual(["evener/update/check"]);
	expect(hook.result.current.getState().check?.currentVersion).toBe("0.9.413");
	await replaced.restart();
	expect(first.calls).toEqual(["evener/update/check"]);
	hook.unmount();
});

it("asks once when the hub comes back from a restart, and shows what that answer says", async () => {
	const h = hub();
	const current = { client: h.client, ready: true };
	const hook = renderHook(() => useHubUpdates(current.client, current.ready));
	await settle();
	const restarting = hook.result.current.restart();
	await settle();
	expect(hook.result.current.getState().restarting).toBe(true);
	h.calls.length = 0;
	h.state.startedAt = SECOND_START;
	current.ready = false;
	hook.rerender();
	current.ready = true;
	hook.rerender();
	await restarting;
	await settle();
	expect(h.calls).toEqual(["evener/update/check"]);
	expect(hook.result.current.getState()).toMatchObject({ restarting: false, check: { startedAt: SECOND_START } });
	hook.unmount();
});
