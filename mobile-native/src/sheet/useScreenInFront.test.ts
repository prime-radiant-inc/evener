import { expect, it, vi } from "vitest";
import { act } from "react-test-renderer";
import { renderHook } from "../renderNative.testkit";
import {
	leaveScreen,
	screenInAncestors,
	screenInFront,
	useScreenInAncestors,
	useScreenInFront,
} from "./useScreenInFront";

const navigationState = vi.hoisted(() => ({
	state: { index: 0, routes: [] as { key: string; name: string }[] },
}));

vi.mock("@react-navigation/native", () => ({
	useNavigationState: <T>(select: (state: typeof navigationState.state) => T) => select(navigationState.state),
}));

const session = { key: "session", name: "Conversation" };
const tasks = { key: "tasks", name: "TasksSheet" };
const reader = { key: "reader", name: "Reader" };

it("keeps a session in front while its own sheet covers it, and not once a screen is pushed", () => {
	navigationState.state = { index: 1, routes: [session, tasks] };
	expect(renderHook(() => useScreenInFront("session")).result.current).toBe(true);
	navigationState.state = { index: 1, routes: [session, reader] };
	expect(renderHook(() => useScreenInFront("session")).result.current).toBe(false);
	// The pushed screen is popped, so session is the top of the stack again.
	navigationState.state = { index: 0, routes: [session] };
	expect(renderHook(() => useScreenInFront("session")).result.current).toBe(true);
});

it("answers the same at the moment of a call", () => {
	const covered = { getState: () => ({ index: 1, routes: [session, tasks] }) };
	expect(screenInFront(covered, "session")).toBe(true);
	const pushed = { getState: () => ({ index: 1, routes: [session, reader] }) };
	expect(screenInFront(pushed, "session")).toBe(false);
	expect(screenInFront({ getState: () => ({ index: 0, routes: [session] }) }, "session")).toBe(true);
});

// A pane can be in front of its own stack while an alert holds another
// conversation over the stack that owns it. Reading a covered pane keeps
// working, but telling the hub the reader saw the reply must not: the answer
// has to include every stack above, not just this one.
//
// The stack above is not this screen's own, so it cannot see that push: the
// answer has to come from a subscription on the stacks above, and it is the
// subscription that makes a cover or a reveal render at all. Called directly,
// these helpers re-read the covering stack on every call and would answer
// correctly with nothing listening, so the hook is what is exercised here.
it("counts the stacks above, so a covered pane is not the reader's view", () => {
	const above = {
		state: {
			index: 0,
			routes: [
				// The Duo split: this conversation's stack is nested inside the
				// root stack's Sessions route.
				{ key: "sessions", name: "Sessions", state: { index: 0, routes: [session] } },
			] as { key: string; name: string; state?: { index: number; routes: { key: string; name: string }[] } }[],
		},
		listeners: new Set<() => void>(),
	};
	const navigation = {
		getState: () => ({ index: 0, routes: [session] }),
		getParent: () => ({
			getState: () => above.state,
			addListener: (_type: "state", listener: () => void) => {
				above.listeners.add(listener);
				return () => above.listeners.delete(listener);
			},
		}),
	};
	// The stack above holds this conversation: the reader is looking at it.
	expect(screenInAncestors(navigation, "session")).toBe(true);
	const seen = renderHook(() => useScreenInAncestors(navigation, "session"));
	expect(seen.result.current).toBe(true);
	// An alert pushes another conversation over it. Nothing in this screen's
	// own stack changed, so only the subscription can bring the news.
	above.state = {
		index: 1,
		routes: [{ key: "sessions", name: "Sessions", state: { index: 0, routes: [session] } }, reader],
	} as typeof above.state;
	expect(screenInAncestors(navigation, "session")).toBe(false);
	// With nothing listening this stays true: the hook's own answer only
	// changes because the subscription asked it to look again.
	act(() => {
		for (const listener of [...above.listeners]) listener();
	});
	expect(seen.result.current).toBe(false);
	expect(screenInFront(navigation, "session")).toBe(false);
	// The alert closes, and the same pane is the reader's view again.
	above.state = {
		index: 0,
		routes: [{ key: "sessions", name: "Sessions", state: { index: 0, routes: [session] } }],
	} as typeof above.state;
	act(() => {
		for (const listener of [...above.listeners]) listener();
	});
	expect(seen.result.current).toBe(true);
	expect(screenInFront(navigation, "session")).toBe(true);
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
