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
