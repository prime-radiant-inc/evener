import { expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { leaveScreen, screenInFront, useScreenInFront } from "./useScreenInFront";

const navigationState = vi.hoisted(() => ({
	focused: false,
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

vi.mock("@react-navigation/native", () => ({
	useIsFocused: () => navigationState.focused,
	useNavigationState: <T>(select: (state: typeof navigationState.state) => T) => select(navigationState.state),
}));

const session = { key: "session", name: "Conversation" };
const tasks = { key: "tasks", name: "TasksSheet" };
const reader = { key: "reader", name: "Reader" };

it("keeps a session in front while its own sheet covers it, and not once a screen is pushed", () => {
	navigationState.focused = false;
	navigationState.state = { index: 1, routes: [session, tasks] };
	expect(renderHook(() => useScreenInFront("session")).result.current).toBe(true);
	navigationState.state = { index: 1, routes: [session, reader] };
	expect(renderHook(() => useScreenInFront("session")).result.current).toBe(false);
	navigationState.focused = true;
	expect(renderHook(() => useScreenInFront("session")).result.current).toBe(true);
});

it("answers the same at the moment of a call", () => {
	const covered = { isFocused: () => false, getState: () => ({ index: 1, routes: [session, tasks] }) };
	expect(screenInFront(covered, "session")).toBe(true);
	const pushed = { isFocused: () => false, getState: () => ({ index: 1, routes: [session, reader] }) };
	expect(screenInFront(pushed, "session")).toBe(false);
	expect(screenInFront({ isFocused: () => true, getState: () => ({ index: 0, routes: [] }) }, "session")).toBe(true);
});

it("leaves with the sheets over the screen, since a covered screen's goBack pops only its top sheet", () => {
	const leave = (index: number, routes: { key: string; name: string }[]) => {
		const pop = vi.fn();
		leaveScreen({ getState: () => ({ index, routes }), pop }, "session");
		return pop.mock.calls;
	};
	expect(leave(1, [reader, session])).toEqual([[1]]);
	expect(leave(2, [reader, session, tasks])).toEqual([[2]]);
	expect(leave(1, [reader, tasks])).toEqual([]);
	expect(leave(0, [reader, session])).toEqual([]);
});
