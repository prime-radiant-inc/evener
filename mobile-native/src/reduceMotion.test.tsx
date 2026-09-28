import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { useReduceMotion } from "./reduceMotion";
import { renderHook } from "./renderNative.testkit";

const accessibility = vi.hoisted(() => ({
	listener: null as ((value: boolean) => void) | null,
	removed: 0,
}));
vi.mock("react-native", () => ({
	AccessibilityInfo: {
		isReduceMotionEnabled: () => Promise.resolve(true),
		addEventListener: (_event: string, listener: (value: boolean) => void) => {
			accessibility.listener = listener;
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
