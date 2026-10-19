import { expect, it } from "vitest";
import { singleFlight } from "./singleFlight";

const tick = () => new Promise((resolve) => setTimeout(resolve));

it("coalesces requests made during a run into one trailing run", async () => {
	const runs: (() => void)[] = [];
	const flight = singleFlight(() => new Promise<void>((resolve) => runs.push(resolve)));
	flight.request();
	flight.request();
	flight.request();
	expect(runs).toHaveLength(1);
	expect(flight.running).toBe(true);
	runs[0]();
	await tick();
	expect(runs).toHaveLength(2);
	runs[1]();
	await tick();
	expect(runs).toHaveLength(2);
	expect(flight.running).toBe(false);
});

it("waits while the owner is busy and runs when drained", () => {
	let busy = true;
	const runs: (() => void)[] = [];
	const flight = singleFlight(
		() => new Promise<void>((resolve) => runs.push(resolve)),
		() => !busy,
	);
	flight.request();
	expect(runs).toHaveLength(0);
	busy = false;
	flight.drain();
	expect(runs).toHaveLength(1);
	flight.drain();
	expect(runs).toHaveLength(1);
});

it("recovers from a run that throws synchronously", () => {
	let calls = 0;
	const flight = singleFlight(() => {
		calls++;
		if (calls === 1) throw new Error("bad run");
		return Promise.resolve();
	});
	expect(() => flight.request()).toThrow("bad run");
	expect(flight.running).toBe(false);
	flight.request();
	expect(calls).toBe(2);
});

it("abandons a run that never settles and starts the next request", async () => {
	const runs: (() => void)[] = [];
	const flight = singleFlight(() => new Promise<void>((resolve) => runs.push(resolve)));
	flight.request();
	expect(flight.running).toBe(true);
	flight.abandon();
	expect(flight.running).toBe(false);
	flight.request();
	expect(runs).toHaveLength(2);
	// The abandoned run's late settle must not clear the replacement's flags.
	runs[0]();
	await tick();
	expect(flight.running).toBe(true);
	runs[1]();
	await tick();
	expect(flight.running).toBe(false);
});

it("resolves settled waiters when settle() drops a satisfied request", async () => {
	// The owner is busy with its own request (idle() false), so a queued
	// request cannot start; the owner then satisfies that work itself
	// (settle). The queue empties without any run finishing — a waiter must
	// hear the flight went idle rather than hang forever.
	let ownerBusy = false;
	let release!: (value: unknown) => void;
	const flight = singleFlight(
		() => new Promise((done) => (release = done)),
		() => !ownerBusy,
	);
	flight.request();
	ownerBusy = true;
	// The run finishes while the owner stays busy, so the trailing request
	// stays queued behind the owner's work.
	const settled = flight.settled();
	release(undefined);
	await tick();
	flight.settle();
	ownerBusy = false;
	await expect(settled).resolves.toBeUndefined();
});
