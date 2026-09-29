// Whether the software keyboard is up, from React Native's Keyboard events:
// Will* on iOS, so layouts move with the keyboard, and Did* on Android, which
// has no Will* events.
import { act } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Platform } from "react-native";
import { composerFocusedAs, keyboard, renderHook } from "./renderNative.testkit";
import { useComposerTyping, useKeyboardShown } from "./useKeyboardShown";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

const platform = Platform as { OS: string };
afterEach(() => {
	keyboard.reset();
	platform.OS = "ios";
});

describe("useKeyboardShown", () => {
	it("follows the keyboard's Will* events on iOS", () => {
		const { result } = renderHook(useKeyboardShown);
		expect(result.current).toBe(false);
		act(() => keyboard.emit("keyboardWillShow"));
		expect(result.current).toBe(true);
		act(() => keyboard.emit("keyboardWillHide"));
		expect(result.current).toBe(false);
	});

	it("follows the Did* events on Android, which has no Will* events", () => {
		platform.OS = "android";
		const { result } = renderHook(useKeyboardShown);
		act(() => keyboard.emit("keyboardWillShow"));
		expect(result.current).toBe(false);
		act(() => keyboard.emit("keyboardDidShow"));
		expect(result.current).toBe(true);
		act(() => keyboard.emit("keyboardDidHide"));
		expect(result.current).toBe(false);
	});

	it("starts shown when the keyboard is already up", () => {
		act(() => keyboard.show());
		expect(renderHook(useKeyboardShown).result.current).toBe(true);
	});

	it("stops listening once unmounted", () => {
		const events = ["keyboardWillShow", "keyboardWillHide"];
		const before = events.map((event) => keyboard.listening(event));
		const { unmount } = renderHook(useKeyboardShown);
		expect(events.map((event) => keyboard.listening(event))).toEqual(before.map((count) => count + 1));
		unmount();
		expect(events.map((event) => keyboard.listening(event))).toEqual(before);
	});
});

describe("useComposerTyping", () => {
	it("is typing only while the keyboard is up and the composer has focus", () => {
		const focus = composerFocusedAs(true);
		const composers = renderHook(() => useComposerTyping(focus));
		const someoneElses = renderHook(() => useComposerTyping(composerFocusedAs(false)));
		const noComposer = renderHook(() => useComposerTyping(undefined));
		expect(composers.result.current).toBe(false);
		act(() => keyboard.show());
		expect(composers.result.current).toBe(true);
		expect(someoneElses.result.current).toBe(false);
		expect(noComposer.result.current).toBe(false);
		// Focus moving to another field with the keyboard still up.
		act(() => focus.set(false));
		expect(composers.result.current).toBe(false);
		act(() => focus.set(true));
		expect(composers.result.current).toBe(true);
		act(() => keyboard.hide());
		expect(composers.result.current).toBe(false);
	});
});
