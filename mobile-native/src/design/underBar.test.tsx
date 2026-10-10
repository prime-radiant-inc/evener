// A list that runs under a bottom bar (the session's bar, the Board's
// toolbar): how it keeps its end clear of the bar, and the bar's height.
import { act } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Platform } from "react-native";
import { renderHook, unmountMountedTrees } from "../renderNative.testkit";
import { listContentMinHeight, underBar, useBarHeight } from "./underBar";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const platform = Platform as { OS: string };
afterEach(() => {
	unmountMountedTrees();
	platform.OS = "ios";
});

describe("underBar", () => {
	it("insets the list by the bar on iOS, so its content size never depends on the bar", () => {
		expect(underBar(180)).toEqual({
			contentInset: { bottom: 180 },
			scrollIndicatorInsets: { bottom: 180 },
			endPadding: 0,
		});
	});

	it("pads the list's end by the bar on Android, which has no content inset", () => {
		platform.OS = "android";
		expect(underBar(180)).toEqual({ scrollIndicatorInsets: { bottom: 180 }, endPadding: 180 });
	});
});

describe("listContentMinHeight", () => {
	it("is the viewport above the bar's inset on iOS", () => {
		expect(listContentMinHeight(600, underBar(150))).toBe(450);
	});

	it("is the whole viewport on Android, whose content pads its own end by the bar", () => {
		platform.OS = "android";
		expect(listContentMinHeight(600, underBar(150))).toBe(600);
	});

	it("is never negative, before the viewport has laid out", () => {
		expect(listContentMinHeight(0, underBar(150))).toBe(0);
	});
});

describe("useBarHeight", () => {
	it("is unknown until the bar lays out, then its height", () => {
		const hook = renderHook(useBarHeight);
		expect(hook.result.current.height).toBeNull();
		act(() =>
			hook.result.current.onLayout({ nativeEvent: { layout: { x: 0, y: 0, width: 390, height: 84 } } } as never),
		);
		expect(hook.result.current.height).toBe(84);
	});
});
