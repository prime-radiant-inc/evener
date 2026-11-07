// The Duo split's own behavior: a Board tap replaces the detail pane's
// conversation instead of stacking another, backing the pane out to its
// placeholder folds nothing, and folding hands the conversation that was on
// screen to the root stack. The navigators are React Navigation's real core
// (native-stack is not renderable under vitest); only the screens are mocked.
import { BaseNavigationContainer } from "@react-navigation/core";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createTestStack } from "../navigationTestStack.testkit";
import { render, settle } from "../renderNative.testkit";
import type { Routes } from "../screens";
import { DuoSplit } from "./DuoSplit";

const harness = vi.hoisted(() => ({
	openConversation: null as ((params: Routes["Conversation"]) => void) | null,
	detailNavigation: null as {
		goBack: () => void;
		getState: () => { index: number; routes: { name: string; params?: unknown }[] };
	} | null,
	onFold: null as ((params: Routes["Conversation"]) => void) | null,
}));

vi.mock("@react-navigation/native", async () => {
	const core = await import("@react-navigation/core");
	return { useNavigation: core.useNavigation, useNavigationState: core.useNavigationState };
});
// The split's navigator runs on core's stack router, as the app's does on
// native-stack's: same routes, same reset, same nested state.
vi.mock("@react-navigation/native-stack", async () => {
	const { createTestStack } = await import("../navigationTestStack.testkit");
	return { createNativeStackNavigator: createTestStack };
});
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ width: 669, height: 900, fontScale: 1, scale: 2 }),
}));

// The Board is the split's root screen; a tap in it is what opens a
// conversation, so the mock hands that callback to the test.
vi.mock("../board/BoardScreen", () => ({
	BoardScreen: ({ onOpenConversation }: { onOpenConversation: (params: Routes["Conversation"]) => void }) => {
		harness.openConversation = onOpenConversation;
		return null;
	},
}));
// The conversation is the detail pane's live screen; the test reads the
// detail stack through its navigation object.
vi.mock("../screens", async () => {
	const { useNavigation } = await import("@react-navigation/core");
	return {
		ConversationScreen: () => {
			harness.detailNavigation = useNavigation() as unknown as typeof harness.detailNavigation;
			return null;
		},
	};
});
vi.mock("../ForkScreen", () => ({ ForkScreen: () => null }));
vi.mock("../PinAssignmentScreen", () => ({ PinAssignmentScreen: () => null }));
vi.mock("../ProjectsScreen", () => ({ SessionLocationScreen: () => null }));
vi.mock("../QueueSheet", () => ({ QueueSheet: () => null }));
vi.mock("../reader/CommentSheet", () => ({ CommentSheet: () => null }));
vi.mock("../reader/CommentsSheet", () => ({ CommentsSheet: () => null }));
vi.mock("../reader/FilesSheet", () => ({ FilesSheet: () => null }));
vi.mock("../reader/OutlineSheet", () => ({ OutlineSheet: () => null }));
vi.mock("../reader/ReaderScreen", () => ({ ReaderScreen: () => null }));
vi.mock("../reader/ReviewSheet", () => ({ ReviewSheet: () => null }));
vi.mock("../SessionDeletionScreen", () => ({ SessionDeletionScreen: () => null }));
vi.mock("../session/CommandsSheet", () => ({ CommandsSheet: () => null }));
vi.mock("../session/ModelSheet", () => ({ ModelSheet: () => null }));
vi.mock("../session/NotesSheet", () => ({ NotesSheet: () => null }));
vi.mock("../session/SessionInfoSheet", () => ({ SessionInfoSheet: () => null }));
vi.mock("../subagents/ActivityScreen", () => ({ ActivityScreen: () => null }));
vi.mock("../subagents/ShellJobScreen", () => ({ ShellJobScreen: () => null }));
vi.mock("../subagents/StopSubagentSheet", () => ({ StopSubagentSheet: () => null }));
vi.mock("../subagents/SubagentScreen", () => ({ SubagentScreen: () => null }));
vi.mock("../TasksSheet", () => ({ TasksSheet: () => null }));

const Root = createTestStack();

function SessionsScreen(props: NativeStackScreenProps<Routes, "Sessions">) {
	return <DuoSplit {...props} onFold={harness.onFold ?? undefined} />;
}

function mount() {
	const onFold = vi.fn();
	harness.onFold = onFold;
	const tree = render(
		<BaseNavigationContainer>
			<Root.Navigator initialRouteName="Sessions">
				<Root.Screen name="Sessions" component={SessionsScreen} />
			</Root.Navigator>
		</BaseNavigationContainer>,
	);
	return { tree, onFold };
}

function open(params: Routes["Conversation"]) {
	const board = harness.openConversation;
	if (!board) throw new Error("the Board offered no way to open a conversation");
	act(() => board(params));
}

function detailState() {
	const navigation = harness.detailNavigation;
	if (!navigation) throw new Error("the detail pane opened no conversation");
	return navigation.getState();
}

const first = { hubId: "hub-1", ref: "local:one", title: "One" };
const second = { hubId: "hub-1", ref: "local:two", title: "Two" };

it("replaces the detail pane's conversation instead of stacking another", async () => {
	mount();
	await settle();
	open(first);
	await settle();
	open(second);
	await settle();
	const state = detailState();
	expect(state.routes.map((route) => route.name)).toEqual(["Sessions", "Conversation"]);
	expect(state.routes[1]?.params).toEqual(second);
});

it("folds nothing when the pane is back on its placeholder", async () => {
	const { tree, onFold } = mount();
	await settle();
	open(first);
	await settle();
	act(() => harness.detailNavigation?.goBack());
	await settle();
	expect(detailState().index).toBe(0);
	act(() => tree.unmount());
	expect(onFold).not.toHaveBeenCalled();
});

it("folds the conversation on screen into the root stack", async () => {
	const { tree, onFold } = mount();
	await settle();
	open(first);
	await settle();
	act(() => tree.unmount());
	expect(onFold).toHaveBeenCalledTimes(1);
	expect(onFold).toHaveBeenCalledWith(first);
});
