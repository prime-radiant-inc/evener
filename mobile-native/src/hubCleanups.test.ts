import { expect, it, vi } from "vitest";
import { runHubCleanups } from "./hubCleanups";

it("runs every cleanup even when an earlier one throws, then rethrows the first failure", () => {
	const first = vi.fn(() => {
		throw new Error("board storage failed");
	});
	const second = vi.fn();
	const third = vi.fn(() => {
		throw new Error("detail-level storage failed");
	});

	expect(() => runHubCleanups("hub-1", [first, second, third])).toThrow("board storage failed");
	expect(first).toHaveBeenCalledWith("hub-1");
	expect(second).toHaveBeenCalledWith("hub-1");
	expect(third).toHaveBeenCalledWith("hub-1");
});

it("does nothing when every cleanup succeeds", () => {
	const cleanups = [vi.fn(), vi.fn()];
	expect(() => runHubCleanups("hub-1", cleanups)).not.toThrow();
	for (const cleanup of cleanups) expect(cleanup).toHaveBeenCalledWith("hub-1");
});

it("still reports a failure when the only failing cleanup throws undefined, a legal throw value", () => {
	// A sentinel not shaped like "no error happened", to tell "the function
	// threw undefined" apart from "the function didn't throw at all" - the
	// exact distinction the fix has to preserve.
	const NOTHING_THROWN = Symbol("nothing thrown");
	const succeeds = vi.fn();
	let caught: unknown = NOTHING_THROWN;
	try {
		runHubCleanups("hub-1", [
			() => {
				// biome-ignore lint/style/noThrowLiteral: proving `undefined` isn't mistaken for "no failure"
				throw undefined;
			},
			succeeds,
		]);
	} catch (error) {
		caught = error;
	}
	expect(caught).not.toBe(NOTHING_THROWN);
	expect(succeeds).toHaveBeenCalledWith("hub-1");
});
