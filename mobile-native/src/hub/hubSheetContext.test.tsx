import { expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { useClosesOnHubChange } from "./hubSheetContext";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

function mount(first: string) {
	const current = { hubId: first };
	const close = vi.fn();
	const leave = vi.fn();
	const hook = renderHook(() => useClosesOnHubChange(current.hubId, close, leave));
	const become = (hubId: string) => {
		current.hubId = hubId;
		hook.rerender();
	};
	return { close, leave, become };
}

it("stays open while the selected hub is the one it opened for", () => {
	const { close, leave, become } = mount("hub-1");
	become("hub-1");
	expect(close).not.toHaveBeenCalled();
	expect(leave).not.toHaveBeenCalled();
});

it("closes once when another hub is selected", () => {
	const { close, leave, become } = mount("hub-1");
	become("hub-2");
	become("hub-3");
	expect(close).toHaveBeenCalledTimes(1);
	expect(leave).not.toHaveBeenCalled();
});

it("leaves for the first-run screen once when no hub is selected", () => {
	const { close, leave, become } = mount("hub-1");
	become("");
	become("");
	expect(leave).toHaveBeenCalledTimes(1);
	expect(close).not.toHaveBeenCalled();
});

it("leaves at once when it opens with no hub selected", () => {
	const { close, leave } = mount("");
	expect(leave).toHaveBeenCalledTimes(1);
	expect(close).not.toHaveBeenCalled();
});
