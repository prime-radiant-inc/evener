import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { useReduceMotion, useReduceTransparency } from "./accessibilitySettings";
import { renderHook } from "./renderNative.testkit";

const accessibility = vi.hoisted(() => ({
	listener: null as ((value: boolean) => void) | null,
	event: null as string | null,
	removed: 0,
	read: null as Promise<boolean> | null,
	transparencyRead: null as Promise<boolean> | null,
}));
vi.mock("react-native", () => ({
	AccessibilityInfo: {
		isReduceMotionEnabled: () => accessibility.read ?? Promise.resolve(true),
		isReduceTransparencyEnabled: () => accessibility.transparencyRead ?? Promise.resolve(true),
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

it("says Reduce Transparency is unknown until its read answers, so glass waits for it", async () => {
	// A fresh launch: nothing known yet.
	vi.resetModules();
	const { useReduceTransparency } = await import("./accessibilitySettings");
	let answer!: (value: boolean) => void;
	accessibility.transparencyRead = new Promise((resolve) => {
		answer = resolve;
	});
	try {
		const hook = renderHook(() => useReduceTransparency());
		expect(hook.result.current).toBeNull();
		await act(async () => answer(false));
		expect(hook.result.current).toBe(false);
		hook.unmount();
	} finally {
		accessibility.transparencyRead = null;
	}
});

it("stays unknown when the read fails, and says nothing of the failure", async () => {
	vi.resetModules();
	const { useReduceTransparency } = await import("./accessibilitySettings");
	accessibility.transparencyRead = Promise.reject(new Error("no accessibility manager"));
	try {
		const hook = renderHook(() => useReduceTransparency());
		await act(async () => {});
		expect(hook.result.current).toBeNull();
		hook.unmount();
	} finally {
		accessibility.transparencyRead = null;
	}
});

it("starts a later mount from the value an earlier mount is still following", async () => {
	// The Board's bar stays mounted under a session it pushed.
	const earlier = renderHook(() => useReduceTransparency());
	await act(async () => {});
	let answer!: (value: boolean) => void;
	accessibility.transparencyRead = new Promise((resolve) => {
		answer = resolve;
	});
	try {
		const later = renderHook(() => useReduceTransparency());
		// Known at once, before its own read answers.
		expect(later.result.current).toBe(true);
		await act(async () => answer(true));
		later.unmount();
		earlier.unmount();
	} finally {
		accessibility.transparencyRead = null;
	}
});

it("forgets the value once nothing follows it, since it could change unheard", async () => {
	const first = renderHook(() => useReduceTransparency());
	await act(async () => {});
	expect(first.result.current).toBe(true);
	first.unmount();
	let answer!: (value: boolean) => void;
	accessibility.transparencyRead = new Promise((resolve) => {
		answer = resolve;
	});
	try {
		const later = renderHook(() => useReduceTransparency());
		expect(later.result.current).toBeNull();
		await act(async () => answer(false));
		expect(later.result.current).toBe(false);
		later.unmount();
	} finally {
		accessibility.transparencyRead = null;
	}
});

it("remembers nothing from a read that answers after its mount is gone", async () => {
	vi.resetModules();
	const { useReduceTransparency } = await import("./accessibilitySettings");
	let answer!: (value: boolean) => void;
	accessibility.transparencyRead = new Promise((resolve) => {
		answer = resolve;
	});
	try {
		const gone = renderHook(() => useReduceTransparency());
		gone.unmount();
		await act(async () => answer(false));
		accessibility.transparencyRead = new Promise(() => {});
		const later = renderHook(() => useReduceTransparency());
		expect(later.result.current).toBeNull();
		later.unmount();
	} finally {
		accessibility.transparencyRead = null;
	}
});
