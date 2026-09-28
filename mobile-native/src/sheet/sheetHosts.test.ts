import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { sheetHosts, sheetKey, useProvideSheetHost, useSheetHost } from "./sheetHosts";

describe("sheet hosts", () => {
	it("answers with the newest owner's host, and the older one once that owner leaves", () => {
		const hosts = sheetHosts<string>();
		const lower = {};
		const upper = {};
		hosts.provide("s", lower, "lower screen");
		hosts.provide("s", upper, "upper screen");
		expect(hosts.get("s")).toBe("upper screen");
		hosts.provide("s", lower, "lower screen, updated");
		expect(hosts.get("s")).toBe("upper screen");
		hosts.release("s", upper);
		expect(hosts.get("s")).toBe("lower screen, updated");
		hosts.release("s", lower);
		expect(hosts.get("s")).toBeUndefined();
	});

	it("keeps keys apart, and tells subscribers only about real changes", () => {
		const hosts = sheetHosts<string>();
		const listener = vi.fn();
		const unsubscribe = hosts.subscribe(listener);
		const owner = {};
		hosts.provide("a", owner, "host a");
		hosts.provide("a", owner, "host a");
		hosts.release("b", owner);
		expect(hosts.get("b")).toBeUndefined();
		expect(listener).toHaveBeenCalledTimes(1);
		unsubscribe();
		hosts.release("a", owner);
		expect(listener).toHaveBeenCalledTimes(1);
	});

	it("builds keys no two sets of parts share", () => {
		expect(sheetKey("hub-1", "local:s1")).not.toBe(sheetKey("hub-1:local", "s1"));
		expect(sheetKey("hub-1")).toBe(sheetKey("hub-1"));
	});
});

describe("providing and finding a host", () => {
	it("provides a screen's host, replaces it without a gap, and takes it away when the screen closes", () => {
		const hosts = sheetHosts<{ text: string }>();
		const seen: (string | undefined)[] = [];
		hosts.subscribe(() => seen.push(hosts.get("s")?.text));
		let host = { text: "first" };
		const screen = renderHook(() => useProvideSheetHost(hosts, "s", host));
		expect(hosts.get("s")).toBe(host);
		host = { text: "second" };
		screen.rerender();
		screen.unmount();
		expect(seen).toEqual(["first", "second", undefined]);
	});

	it("provides nothing while the screen has no host yet, and takes a host away when it has none again", () => {
		const hosts = sheetHosts<{ text: string }>();
		// The same session open lower in the stack keeps answering until this
		// copy has something to say.
		const lower = { text: "lower screen" };
		hosts.provide("s", {}, lower);
		let host: { text: string } | undefined;
		const screen = renderHook(() => useProvideSheetHost(hosts, "s", host));
		expect(hosts.get("s")).toBe(lower);
		host = { text: "loaded" };
		screen.rerender();
		expect(hosts.get("s")).toBe(host);
		host = undefined;
		screen.rerender();
		expect(hosts.get("s")).toBe(lower);
		screen.unmount();
	});

	it("hands a sheet its screen's host, and closes the sheet once the screen is gone", () => {
		const hosts = sheetHosts<string>();
		const owner = {};
		hosts.provide("s", owner, "live");
		const sheet = { finish: vi.fn() };
		const view = renderHook(() => useSheetHost(hosts, "s", sheet));
		expect(view.result.current).toBe("live");
		expect(sheet.finish).not.toHaveBeenCalled();
		act(() => hosts.release("s", owner));
		expect(view.result.current).toBeUndefined();
		expect(sheet.finish).toHaveBeenCalledOnce();
	});
});
