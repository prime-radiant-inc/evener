import { expect, it } from "vitest";
import { encodeToFit, fitAttempts } from "./imageFit";

it("scales a large photo down along its long edge, one step at a time", () => {
	expect(fitAttempts(4032, 3024)).toEqual([
		{ resize: { width: 2048 }, compress: 0.85 },
		{ resize: { width: 1536 }, compress: 0.75 },
		{ resize: { width: 1024 }, compress: 0.7 },
	]);
	expect(fitAttempts(3024, 4032)[0]).toEqual({ resize: { height: 2048 }, compress: 0.85 });
});

it("never enlarges a small image", () => {
	expect(fitAttempts(800, 600)).toEqual([
		{ resize: null, compress: 0.85 },
		{ resize: null, compress: 0.75 },
		{ resize: null, compress: 0.7 },
	]);
	expect(fitAttempts(1800, 1200)[0]).toEqual({ resize: null, compress: 0.85 });
	expect(fitAttempts(1800, 1200)[1]).toEqual({ resize: { width: 1536 }, compress: 0.75 });
});

it("keeps the first encoding that fits, and tries no further", async () => {
	const tried: number[] = [];
	const sizes = [12, 6, 3];
	const result = await encodeToFit(
		4032,
		3024,
		8,
		(data) => data.length,
		async (attempt) => {
			tried.push(attempt.compress);
			return "x".repeat(sizes[tried.length - 1] ?? 0);
		},
	);
	expect(result).toBe("x".repeat(6));
	expect(tried).toEqual([0.85, 0.75]);
});

it("hands back the smallest try when nothing fits, for the size check to name", async () => {
	const result = await encodeToFit(
		4032,
		3024,
		2,
		(data) => data.length,
		async (attempt) => "x".repeat(attempt.compress === 0.7 ? 5 : 9),
	);
	expect(result).toBe("x".repeat(5));
});
