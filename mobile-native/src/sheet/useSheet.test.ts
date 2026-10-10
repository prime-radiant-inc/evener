import { act } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { alertRequests, renderHook } from "../renderNative.testkit";
import { useSheet } from "./useSheet";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), dispatch: vi.fn() }));
const guard = vi.hoisted(() => ({
	prevented: false,
	onPrevent: null as null | ((options: { data: { action: unknown } }) => void),
}));

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: (prevent: boolean, callback: (options: { data: { action: unknown } }) => void) => {
		guard.prevented = prevent;
		guard.onPrevent = callback;
	},
}));

const swipeDown = { type: "POP", payload: { count: 1 } };

beforeEach(() => {
	navigation.goBack.mockClear();
	navigation.dispatch.mockClear();
	guard.onPrevent = null;
	alertRequests.length = 0;
});

describe("closing a sheet (spec 6)", () => {
	it("closes a sheet with nothing unsaved at once", () => {
		const sheet = renderHook(() => useSheet());
		expect(guard.prevented).toBe(false);
		sheet.result.current.close();
		expect(navigation.goBack).toHaveBeenCalledOnce();
		expect(alertRequests).toEqual([]);
	});

	it("asks before discarding unsaved input, and only Discard lets the sheet go", () => {
		renderHook(() => useSheet({ dirty: true, discardTitle: "Discard this comment?" }));
		expect(guard.prevented).toBe(true);
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(alertRequests).toHaveLength(1);
		const [ask] = alertRequests;
		expect(ask?.title).toBe("Discard this comment?");
		expect(ask?.buttons?.map((button) => button.text)).toEqual(["Keep editing", "Discard"]);
		ask?.buttons?.[0]?.onPress?.();
		expect(navigation.dispatch).not.toHaveBeenCalled();
		ask?.buttons?.[1]?.onPress?.();
		expect(navigation.dispatch).toHaveBeenCalledWith(swipeDown);
	});

	it("waits out a write in flight without asking, unless the write finishes the sheet", () => {
		const sheet = renderHook(() => useSheet({ dirty: true, busy: true }));
		expect(guard.prevented).toBe(true);
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(alertRequests).toEqual([]);
		expect(navigation.dispatch).not.toHaveBeenCalled();
		sheet.result.current.finish();
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(navigation.dispatch).toHaveBeenCalledWith(swipeDown);
	});

	it("leaves without asking when it finishes on purpose", () => {
		const sheet = renderHook(() => useSheet({ dirty: true }));
		sheet.result.current.finish();
		expect(navigation.goBack).toHaveBeenCalledOnce();
		const pop = { type: "GO_BACK" };
		act(() => guard.onPrevent?.({ data: { action: pop } }));
		expect(navigation.dispatch).toHaveBeenCalledWith(pop);
		expect(alertRequests).toEqual([]);
	});

	it("resets finishing once it leaves, so a second finish is judged on its own", () => {
		const sheet = renderHook(() => useSheet({ dirty: true }));
		sheet.result.current.finish();
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(alertRequests).toEqual([]);

		// A stale `finishing` flag would still read true here, bypassing the
		// prompt for a swipe that never called finish().
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(alertRequests).toHaveLength(1);

		// A genuine second finish still bypasses the prompt, on its own merits.
		navigation.dispatch.mockClear();
		sheet.result.current.finish();
		act(() => guard.onPrevent?.({ data: { action: swipeDown } }));
		expect(navigation.dispatch).toHaveBeenCalledWith(swipeDown);
		expect(alertRequests).toHaveLength(1);
	});

	it("lets `then` remove the sheet when it leads somewhere else", () => {
		const sheet = renderHook(() => useSheet({ dirty: true }));
		const then = vi.fn();
		sheet.result.current.finish(then);
		expect(then).toHaveBeenCalledOnce();
		expect(navigation.goBack).not.toHaveBeenCalled();
	});

	it("tells its owner once, when it goes away", () => {
		const onClosed = vi.fn();
		const sheet = renderHook(() => useSheet({ onClosed }));
		sheet.rerender();
		expect(onClosed).not.toHaveBeenCalled();
		sheet.unmount();
		expect(onClosed).toHaveBeenCalledOnce();
	});
});
