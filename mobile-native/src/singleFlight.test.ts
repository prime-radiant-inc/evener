import { expect, it } from "vitest";
import { singleFlight } from "./singleFlight";

const tick = () => new Promise((resolve) => setTimeout(resolve));

it("coalesces requests made during a run into one trailing run", async () => {
	const runs: (() => void)[] = [];
	const flight = singleFlight(
		() => new Promise<void>((resolve) => runs.push(resolve)),
	);
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
