import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { useReduceMotion, useReduceTransparency } from "./reduceMotion";
import { renderHook } from "./renderNative.testkit";

const accessibility = vi.hoisted(() => ({
	listener: null as ((value: boolean) => void) | null,
	event: null as string | null,
	removed: 0,
	read: null as Promise<boolean> | null,
}));
vi.mock("react-native", () => ({
	AccessibilityInfo: {
		isReduceMotionEnabled: () => accessibility.read ?? Promise.resolve(true),
		isReduceTransparencyEnabled: () => Promise.resolve(true),
		addEventListener: (event: string, listener: (value: boolean) => void) => {
			accessibility.listener = listener;
			accessibility.event = event;
			return {
				remove: () => {
					accessibility.removed += 1;
				},
			};
		},
	},
}));

it("reads Reduce Motion at mount and follows it when it changes", async () => {
	const hook = renderHook(() => useReduceMotion());
	await act(async () => {});
	expect(hook.result.current).toBe(true);
	act(() => accessibility.listener?.(false));
	expect(hook.result.current).toBe(false);
	hook.unmount();
	expect(accessibility.removed).toBe(1);
});

it("keeps a change that arrives before the mount-time read answers", async () => {
	let answer!: (value: boolean) => void;
	accessibility.read = new Promise((resolve) => {
		answer = resolve;
	});
	try {
		const hook = renderHook(() => useReduceMotion());
		act(() => accessibility.listener?.(true));
		await act(async () => answer(false));
		expect(hook.result.current).toBe(true);
		hook.unmount();
	} finally {
		accessibility.read = null;
	}
});

it("reads Reduce Transparency at mount and follows it when it changes", async () => {
	const hook = renderHook(() => useReduceTransparency());
	await act(async () => {});
	expect(accessibility.event).toBe("reduceTransparencyChanged");
	expect(hook.result.current).toBe(true);
	act(() => accessibility.listener?.(false));
	expect(hook.result.current).toBe(false);
	hook.unmount();
});
