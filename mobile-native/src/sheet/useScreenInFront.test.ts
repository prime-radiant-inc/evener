import { expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { leaveScreen, screenInFront, useScreenInFront } from "./useScreenInFront";

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

// The Duo split nests a conversation's stack inside the root stack's Sessions
// route, so a push on the root stack covers the pane while the conversation's
// own stack still shows it with its sheet open. The screen's own state alone
// cannot answer this one, and neither can the root's: both have to say the
// screen is still the one in front.
it("follows a conversation nested in a root stack through a push over it", () => {
	const detail = { index: 1, routes: [session, tasks] };
	const root = (index: number) => ({
		index,
		routes:
			index === 0
				? [{ key: "sessions", name: "Sessions", state: detail }]
				: [
						{ key: "sessions", name: "Sessions", state: detail },
						{ key: "reader", name: "Reader" },
					],
	});
	let rootIndex = 0;
	const navigation = {
		getState: () => detail,
		getParent: () => ({ getState: () => root(rootIndex) }),
	};
	expect(screenInFront(navigation, "session")).toBe(true);
	// A root screen is pushed over the pane: this stack's routes are unchanged.
	rootIndex = 1;
	expect(screenInFront(navigation, "session")).toBe(false);
	// Popped, and the conversation is the one in front again.
	rootIndex = 0;
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
