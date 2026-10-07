// The testkit's render/renderHook drive react-test-renderer, which logs a
// deprecation warning through console.error on every create() unless it is
// told it runs in a React Native test environment (vitestSetup.ts sets that
// flag). Left unset, every rendering suite buries real stderr under one line
// per render, so this pins the harness's output as clean (#2433).
import { createElement, useEffect } from "react";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { answered, render, renderHook, unmountMountedTrees } from "./renderNative.testkit";

const DEPRECATION = "react-test-renderer is deprecated";

describe("renderNative.testkit", () => {
	it("renders without logging react-test-renderer's deprecation warning", () => {
		const logged: unknown[][] = [];
		const spy = vi.spyOn(console, "error").mockImplementation((...args: unknown[]) => {
			logged.push(args);
		});
		try {
			render(createElement("View"));
		} finally {
			spy.mockRestore();
		}
		const deprecations = logged.filter((args) =>
			args.some((arg) => typeof arg === "string" && arg.includes(DEPRECATION)),
		);
		expect(deprecations).toEqual([]);
	});
});

// A tree a test mounts is unmounted once that test ends, so its timers and
// subscriptions can't update it after the file's last test, outside act,
// while vitest tears the worker down (#3916, #3924).
describe("renderNative.testkit unmounts ahead of a suite's own cleanup", () => {
	const order: string[] = [];
	afterEach(() => {
		unmountMountedTrees();
		order.push("cleanup");
	});
	function Probe() {
		useEffect(() => () => void order.push("unmounted"), []);
		return null;
	}

	it("unmounts a test's trees when its afterEach asks, before the rest of that afterEach", () => {
		onTestFinished(() => {
			expect(order).toEqual(["unmounted", "cleanup"]);
		});
		render(createElement(Probe));
	});
});

describe("renderNative.testkit unmounts what a test mounted", () => {
	const unmounted: string[] = [];
	afterEach(() => {
		unmounted.push("afterEach");
	});
	function Probe({ name }: { name: string }) {
		useEffect(() => () => void unmounted.push(name), [name]);
		return null;
	}

	it("unmounts a render and a renderHook after the test's afterEach, last mounted first", () => {
		// Registered before the mounts, so it runs after their unmounts
		// (onTestFinished runs last-registered first, after afterEach).
		onTestFinished(() => {
			expect(unmounted).toEqual(["afterEach", "renderHook", "render"]);
		});
		render(createElement(Probe, { name: "render" }));
		renderHook(() => useEffect(() => () => void unmounted.push("renderHook"), []));
		expect(unmounted).toEqual([]);
	});
});

describe("renderNative.testkit's answered native reads", () => {
	it("answer then, catch and finally before the caller's next line", () => {
		const seen: string[] = [];
		void answered(true).then((value) => seen.push(`then:${value}`));
		void answered(false)
			.catch(() => true)
			.then((value) => seen.push(`catch-then:${value}`));
		void answered(1).finally(() => seen.push("finally"));
		expect(seen).toEqual(["then:true", "catch-then:false", "finally"]);
	});

	it("rejects the chain when a callback throws, as a real promise does", async () => {
		const failure = new Error("boom");
		const chained = answered(true).then(() => {
			throw failure;
		});
		await expect(chained).rejects.toBe(failure);
	});

	it("takes then's onRejected without calling it, and follows a returned thenable", async () => {
		const seen: string[] = [];
		void answered(1).then(
			(value) => seen.push(`then:${value}`),
			() => seen.push("rejected"),
		);
		expect(seen).toEqual(["then:1"]);
		const thenable: PromiseLike<string> = {
			then: (resolve) => {
				resolve?.("thenable");
				return thenable as never;
			},
		};
		const followed = answered(1).then(() => thenable);
		await expect(followed).resolves.toBe("thenable");
	});
});
