// The render harness mobile-native's vitest run could not mount a component or
// drive a hook with before this: the package runs in vitest's node environment
// (vitest.config.mts), where React Native's own entry point and the Expo native
// modules it reaches cannot load. react-test-renderer renders a React tree
// without a DOM, so the harness swaps the app's native module surface for inert
// host elements (nativeModuleMock) and lets each test mount the real screen or
// hook with only its native edges mocked.
//
// Nothing here is imported by production code: the module is a .testkit, so
// vitest's default include (only *.test.*) never collects it as a suite.
import { createElement, type ReactElement, type ReactNode } from "react";
import {
	act,
	create,
	type ReactTestRenderer,
	type ReactTestRendererJSON,
} from "react-test-renderer";

// React 19's act() only drives effects when it is told it is inside a test
// environment; vitest is not jest, so nothing sets this for us.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** The slice of the `react-native` module the Providers screen and ui.tsx
 * import, as inert host elements: a host string is a valid element type for
 * react-test-renderer, and its children render as they were passed, so a test
 * can find rendered Text by type and read the whole rendered tree as JSON.
 *
 * SectionList is the one component that is not inert: a real host element
 * would swallow renderItem/renderSectionHeader as props, and the mount-order
 * property this harness exists to pin (the listing the screen publishes under
 * the child's mount effect) is only observable in what those callbacks
 * render. This stub drives them the way the real list does, one item per row.
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
	return {
		ActivityIndicator: "ActivityIndicator",
		Alert: { alert: () => {} },
		Modal: "Modal",
		Platform: { OS: "ios" as const },
		Pressable: "Pressable",
		ScrollView: "ScrollView",
		SectionList,
		StyleSheet: { create: <T,>(styles: T): T => styles, flatten: (style: unknown) => style },
		Text: "Text",
		TextInput: "TextInput",
		View: "View",
		useColorScheme: () => "light" as const,
		useWindowDimensions: () => ({ fontScale: 1, scale: 2, width: 390, height: 844 }),
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
