// The native counterpart to the web pane's timestamps rule (TasksPanel.tsx:
// the completed line renders for done rows only): the store stamps every
// terminal transition (done AND cancelled), so a cancelled row now carries a
// settle stamp on the wire - and must not read as Completed. The guard is the
// sheet's one behavior change from the stamping; the store-side contract is
// pinned in agent/task/task_store_test.go.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { InstanceListResponse } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { TaskList, TasksSheet } from "./TasksSheet";
import { pressable, render, renderedText, screenConnection, scriptedClient } from "./renderNative.testkit";

const harness = vi.hoisted(() => {
	const goBack = vi.fn();
	return {
		connection: {} as Record<string, unknown>,
		goBack,
		// useNavigation hands a screen the same object on every render.
		navigation: { goBack, dispatch: () => {} },
	};
});

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("./MarkdownResponse", () => ({ MarkdownResponse: () => null }));
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => harness.navigation,
	usePreventRemove: () => {},
}));
vi.mock("./ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));

const tasks = {
	data: [
		{
			id: 1,
			type: "implement",
			description: "finish it",
			prompt: "",
			status: "done",
			created_at: "2026-09-23T13:40:00Z",
			updated_at: "2026-09-23T14:59:00Z",
			completed_at: "2026-09-23T14:59:00Z",
		},
		{
			id: 2,
			type: "implement",
			description: "drop it",
			prompt: "",
			status: "cancelled",
			created_at: "2026-09-23T13:40:00Z",
			updated_at: "2026-09-23T14:00:00Z",
			completed_at: "2026-09-23T14:00:00Z",
		},
	],
} as unknown as InstanceListResponse;

function taskList(client: ConversationClientLike, connected: boolean) {
	return <TaskList client={client} sessionRef="local:s" threadId="s" hasTasks connected={connected} />;
}

it("renders the Completed line for done rows only, never a stamped cancellation", async () => {
	const { client } = scriptedClient(tasks);
	const tree = render(taskList(client, true));
	// Let the initial read (store.watch's own refresh) land.
	await act(async () => {});

	// Both rows are terminal, so both sit behind the settled group's toggle.
	const settledToggle = pressable(tree, "Done · settled · 2");
	await act(async () => {
		settledToggle?.props.onPress();
	});

	// The timestamps - the Completed line among them - render inside the
	// row's own expanded body, so open both rows.
	const rowButton = (label: string) =>
		tree.root
			.findAll((node) => node.props.accessibilityRole === "button")
			.find((node) => node.props.accessibilityLabel === label);
	await act(async () => {
		rowButton("Done: finish it")?.props.onPress();
	});
	await act(async () => {
		rowButton("Cancelled: drop it")?.props.onPress();
	});

	const text = renderedText(tree);
	expect(text).toContain("finish it");
	expect(text).toContain("drop it");
	// Exactly one Completed line: the done row's. A cancelled row carrying the
	// new settle stamp must not claim it.
	expect(text.match(/Completed/g)?.length).toBe(1);
});

it("keeps its last list through a dropped connection", async () => {
	const { client } = scriptedClient(tasks);
	const tree = render(taskList(client, true));
	await act(async () => {});
	expect(renderedText(tree)).toContain("Done · settled · 2");

	await act(async () => {
		tree.update(taskList(client, false));
	});

	// The connection dropped, but the store's last loaded rows are untouched:
	// the list is still there, alongside the note explaining why it may be
	// stale. Calm copy (spec principle 2): it says what happened and stops,
	// never asking the person to reconnect - the app does that on its own -
	// so the exact sentence is pinned, and the absence of any reconnect
	// directive is checked directly.
	expect(renderedText(tree)).toContain("Done · settled · 2");
	expect(renderedText(tree)).toContain("Disconnected. The last loaded tasks are shown.");
	expect(renderedText(tree).toLowerCase()).not.toContain("reconnect");
});

type TasksSheetProps = ComponentProps<typeof TasksSheet>;

function tasksRoute(hubId: string): TasksSheetProps["route"] {
	return {
		key: "tasks-sheet",
		name: "TasksSheet",
		params: { hubId, ref: "local:s", threadId: "s", hasTasks: true },
	} as TasksSheetProps["route"];
}

const sheetNavigation = {} as TasksSheetProps["navigation"];

it("opens as a sheet titled Tasks over the route's hub, and Done closes it", async () => {
	harness.goBack.mockClear();
	const { client } = scriptedClient(tasks);
	harness.connection = screenConnection(client, "ready");
	const tree = render(<TasksSheet route={tasksRoute("hub-1")} navigation={sheetNavigation} />);
	await act(async () => {});

	const title = tree.root.find((node) => node.props.accessibilityRole === "header");
	expect(title.props.children).toBe("Tasks");
	// A sheet's header carries only its title: the hub's name is gone.
	expect(renderedText(tree)).not.toContain("Work hub");
	// The list loaded through the hub's client once the screen adopted it.
	expect(renderedText(tree)).toContain("Done · settled · 2");
	// The first render has no adopted client yet; the sheet waits, it doesn't close.
	expect(harness.goBack).not.toHaveBeenCalled();

	const done = tree.root.find(
		(node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel === "Done",
	);
	await act(async () => {
		done.props.onPress();
	});
	expect(harness.goBack).toHaveBeenCalledTimes(1);
});

it("closes itself when the active hub is no longer the route's hub", async () => {
	harness.goBack.mockClear();
	const { client } = scriptedClient(tasks);
	harness.connection = screenConnection(client, "ready");
	const tree = render(<TasksSheet route={tasksRoute("hub-2")} navigation={sheetNavigation} />);
	await act(async () => {});

	expect(renderedText(tree)).toBe("");
	expect(harness.goBack).toHaveBeenCalledTimes(1);
});
