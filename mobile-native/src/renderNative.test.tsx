// The testkit's render/renderHook drive react-test-renderer, which logs a
// deprecation warning through console.error on every create() unless it is
// told it runs in a React Native test environment (vitestSetup.ts sets that
// flag). Left unset, every rendering suite buries real stderr under one line
// per render, so this pins the harness's output as clean (#2433).
import { createElement } from "react";
import { describe, expect, it, vi } from "vitest";
import { render } from "./renderNative.testkit";

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
