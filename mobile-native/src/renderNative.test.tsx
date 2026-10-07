// The testkit's render/renderHook drive react-test-renderer, which logs a
// deprecation warning through console.error on every create() unless it is
// told it runs in a React Native test environment (vitestSetup.ts sets that
// flag). Left unset, every rendering suite buries real stderr under one line
// per render, so this pins the harness's output as clean (#2433).
import { createElement, useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { answered, render, renderHook } from "./renderNative.testkit";

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
describe("renderNative.testkit unmounts what a test mounted", () => {
	const unmounted: string[] = [];
	afterEach(() => {
		unmounted.push("afterEach");
	});
	function Probe({ name }: { name: string }) {
		useEffect(() => () => void unmounted.push(name), [name]);
		return null;
	}

	it("mounts a tree with render and a hook with renderHook", () => {
		render(createElement(Probe, { name: "render" }));
		renderHook(() => useEffect(() => () => void unmounted.push("renderHook"), []));
		expect(unmounted).toEqual([]);
	});

	it("finds both unmounted once that test ended", () => {
		// After the test's afterEach hooks; last mounted, first unmounted, as
		// nested cleanup expects.
		expect(unmounted.slice(0, 3)).toEqual(["afterEach", "renderHook", "render"]);
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
});
