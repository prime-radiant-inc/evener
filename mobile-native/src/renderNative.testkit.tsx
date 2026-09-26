// Rendering a component or hook under vitest needs a renderer this package's
// node environment (vitest.config.mts) does not have: React Native's entry
// point and the Expo native modules it reaches cannot load there.
// react-test-renderer renders a React tree without a DOM, so the harness
// substitutes the app's native module surface with inert host elements
// (nativeModuleMock) and mounts the real screen or hook with only its native
// edges mocked.
//
// Production code never imports this module: it is a .testkit, and vitest's
// default include collects only *.test.* files as suites.
import { createElement, type ReactElement, type ReactNode } from "react";
import {
	act,
	create,
	type ReactTestRenderer,
	type ReactTestRendererJSON,
} from "react-test-renderer";
import type {
	AnyNotification,
	ConnectionState,
	InstanceListResponse,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

// React 19's act() only drives effects when it is told it is inside a test
// environment; vitest is not jest, so nothing sets this for us.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** The slice of the `react-native` module the Providers screen and ui.tsx
 * import, as inert host elements: a host string is a valid element type for
 * react-test-renderer, and its children render as they were passed, so a test
 * can find rendered Text by type and read the whole rendered tree as JSON.
 *
 * SectionList and FlatList are the components that are not inert: a real host
 * element would swallow renderItem/renderSectionHeader as props, and the
 * mount-order property this harness exists to pin (the listing the screen
 * publishes under the child's mount effect) is only observable in what those
 * callbacks render. These stubs drive them the way the real lists do, one
 * item per row.
 */
export function nativeModuleMock() {
	const SectionList = (props: {
		sections: { title: string; data: { name: string }[] }[];
		renderItem?: (info: { item: { name: string } }) => ReactNode;
		renderSectionHeader?: (info: { section: { title: string } }) => ReactNode;
		ListHeaderComponent?: ReactNode;
		ListEmptyComponent?: ReactNode;
	}) =>
		createElement(
			"SectionList",
			null,
			props.ListHeaderComponent ?? null,
			...props.sections.flatMap((section) => [
				props.renderSectionHeader?.({ section }) ?? null,
				...section.data.map((item) =>
					createElement(
						"Item",
						{ key: item.name },
						props.renderItem?.({ item }) ?? null,
					),
				),
			]),
			props.sections.length === 0 ? (props.ListEmptyComponent ?? null) : null,
		);
	const FlatList = (props: {
		data?: unknown[];
		keyExtractor?: (item: unknown, index: number) => string;
		renderItem?: (info: { item: unknown }) => ReactNode;
		ListHeaderComponent?: ReactNode;
		ListEmptyComponent?: ReactNode;
	}) =>
		createElement(
			"FlatList",
			null,
			props.ListHeaderComponent ?? null,
			...(props.data ?? []).map((item, index) =>
				createElement(
					"Item",
					{ key: props.keyExtractor?.(item, index) ?? index },
					props.renderItem?.({ item }) ?? null,
				),
			),
			(props.data ?? []).length === 0 ? (props.ListEmptyComponent ?? null) : null,
		);

	// KeyboardAvoidingView only shifts layout; the test tree renders its
	// children unchanged.
	const KeyboardAvoidingView = (props: { children?: ReactNode }) =>
		createElement("KeyboardAvoidingView", null, props.children);

	return {
		ActivityIndicator: "ActivityIndicator",
		Alert: { alert: recordAlert },
		FlatList,
		KeyboardAvoidingView,
		Modal: "Modal",
		Platform: { OS: "ios" as const },
		Pressable: "Pressable",
		ScrollView: "ScrollView",
		SectionList,
		StyleSheet: { create: <T,>(styles: T): T => styles },
		Switch: "Switch",
		Text: "Text",
		TextInput: "TextInput",
		View: "View",
		useColorScheme: () => "light" as const,
		useWindowDimensions: () => ({ fontScale: 1, scale: 2, width: 390, height: 844 }),
	};
}

/** One Alert.alert call the mounted tree made. */
export interface AlertRequest {
	title: string;
	message?: string;
	buttons?: { text?: string; style?: string; onPress?: () => void }[];
}

/** Every Alert.alert call the mounted tree made, oldest first. A test that
 * drives a confirmation dialog reads the buttons off the request it cares
 * about and invokes the one it wants; production Alert never returns. */
export const alertRequests: AlertRequest[] = [];

function recordAlert(
	title: string,
	message?: string,
	buttons?: AlertRequest["buttons"],
): void {
	alertRequests.push({ title, message, buttons });
}

/** The client a test hands the credential store: every request method it is
 * asked for is recorded in `methods`, every answer is `rows`, and
 * `unsubscribes()` counts the times the store released its notification
 * subscription - the observable behind binding and closing a connection.
 *
 * `script` overrides answers per method: each entry is consumed in order and
 * the last one repeats, so a test can answer the listing read that mounted the
 * screen with rows and the post-write refresh with the rows that write left
 * behind. An Error entry is thrown, which is how a scripted rejection reaches
 * a caller's catch. */
export function scriptedClient(
	rows: InstanceListResponse,
	script: Record<string, (InstanceListResponse | Error)[]> = {},
) {
	const methods: string[] = [];
	const requests: { method: string; params: unknown }[] = [];
	const taken = new Map<string, number>();
	let unsubscribes = 0;
	const client = {
		request: async (method: string, params?: unknown) => {
			methods.push(method);
			requests.push({ method, params });
			const answers = script[method];
			if (!answers || answers.length === 0) return rows;
			const index = Math.min(taken.get(method) ?? 0, answers.length - 1);
			taken.set(method, index + 1);
			const answer = answers[index];
			if (answer instanceof Error) throw answer;
			return answer;
		},
		onNotification: (_handler: (n: AnyNotification) => void) => () => {
			unsubscribes += 1;
		},
	} as ConversationClientLike;
	return { client, methods, requests, unsubscribes: () => unsubscribes };
}

/** The connection value the retained-screen suites report through their
 * mocked ConnectionProvider: the Work hub's profile, its client, the state
 * the test drives, and the fatal flag and manual retry the retained-screen
 * wiring reads. One literal where five suites' fixtures matched field for
 * field (#1942), so the shape the screens read cannot drift between suites -
 * the test-side twin of the useRetainedScreenConnection wiring the screens
 * themselves share (#2164). A test whose scenario needs a different hub,
 * retry or verdict spreads its override over the result, so the outlier
 * stays visible at its use. */
export function screenConnection(
	client: unknown,
	state: ConnectionState,
): Record<string, unknown> {
	return {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client,
		state,
		fatal: false,
		retry: () => {},
	};
}

/** Mounts `element` and flushes its effects, returning the test renderer. */
export function render(element: ReactElement): ReactTestRenderer {
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(element);
	});
	return tree;
}

/** renderHook, in the one shape this package needs: a component that calls
 * `hook()` every render, plus the rerender/unmount the harness drives. */
export function renderHook<T>(hook: () => T): {
	result: { current: T };
	rerender: () => void;
	unmount: () => void;
} {
	const result = { current: undefined as T };
	function Probe() {
		result.current = hook();
		return null;
	}
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(createElement(Probe));
	});
	return {
		result,
		rerender: () => act(() => tree.update(createElement(Probe))),
		unmount: () => act(() => tree.unmount()),
	};
}

/** Every string the mounted tree renders, joined - the cheap way to assert a
 * screen published a row or a diagnostic without querying by component. */
export function renderedText(tree: ReactTestRenderer): string {
	const chunks: string[] = [];
	const visit = (node: ReactTestRendererJSON | string | null) => {
		if (node === null) return;
		if (typeof node === "string") {
			chunks.push(node);
			return;
		}
		for (const child of node.children ?? []) visit(child);
	};
	const json = tree.toJSON();
	if (Array.isArray(json)) for (const node of json) visit(node);
	else visit(json);
	return chunks.join(" ");
}
