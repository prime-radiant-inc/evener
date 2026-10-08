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
import {
	type ComponentType,
	createElement,
	type ForwardedRef,
	forwardRef,
	memo,
	type ReactElement,
	type ReactNode,
	type Ref,
	useEffect,
	useImperativeHandle,
	useRef,
	useState,
} from "react";
import {
	act,
	create,
	type ReactTestInstance,
	type ReactTestRenderer,
	type ReactTestRendererJSON,
	type TestRendererOptions,
} from "react-test-renderer";
import type { AnyNotification, ConnectionState, InstanceListResponse } from "@evener/appwire-client";
import { expect, onTestFinished, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ComposerFocus } from "./session/composerFocus";
import { shrinkingScroller } from "./session/dockCard";

// React 19's act() only drives effects when it is told it is inside a test
// environment; vitest is not jest, so nothing sets this for us.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** A native read that has already answered: a real promise whose then (and
 * so catch and each link after them) runs its callback at once, so a mount's
 * read lands inside the render's act instead of on a microtask after a
 * synchronous test has ended. An await, or a link after finally, still waits
 * a microtask. It never rejects, so then's onRejected is never called; a
 * callback that throws rejects the chain it returns, and one that returns a
 * promise or thenable is followed, as a real promise's would be. */
export function answered<T>(value: T): Promise<T> {
	const promise = Promise.resolve(value);
	promise.then = ((onFulfilled?: ((value: T) => unknown) | null) => {
		try {
			const next = onFulfilled ? onFulfilled(value) : value;
			// Reading then can throw too (a getter); that rejects, as a real
			// promise's resolution does.
			const thenable = typeof (next as { then?: unknown } | null)?.then === "function";
			return thenable ? Promise.resolve(next) : answered(next);
		} catch (error) {
			return Promise.reject(error);
		}
	}) as typeof promise.then;
	return promise;
}

/** The device's system glass and its accessibility settings, as the app
 * reads them: whether the Liquid Glass API is there (expo-glass-effect's
 * isGlassEffectAPIAvailable, faked in vitestSetup.ts), and Reduce
 * Transparency, which a test turns on or off with setReduceTransparency. */
export const systemGlass = (() => {
	const listeners = new Set<(value: boolean) => void>();
	let reduceTransparency = false;
	let pendingAnswer: (() => void) | null = null;
	return {
		/** The next Reduce Transparency read waits until answerRead(). */
		readPending: false,
		/** Answers a read readPending held, with the current setting. */
		answerRead() {
			pendingAnswer?.();
			pendingAnswer = null;
		},
		read(): Promise<boolean> {
			if (!this.readPending) return answered(reduceTransparency);
			return new Promise((resolve) => {
				pendingAnswer = () => resolve(reduceTransparency);
			});
		},
		/** "throws" stands for a binary without the native module, where
		 * reading it throws. */
		available: false as boolean | "throws",
		get reduceTransparency() {
			return reduceTransparency;
		},
		listen(listener: (value: boolean) => void) {
			listeners.add(listener);
			return { remove: () => listeners.delete(listener) };
		},
		setReduceTransparency(value: boolean) {
			reduceTransparency = value;
			for (const listener of listeners) listener(value);
		},
		reset() {
			this.available = false;
			this.readPending = false;
			pendingAnswer = null;
			reduceTransparency = false;
			listeners.clear();
		},
	};
})();

/** The software keyboard as React Native's Keyboard module reports it: a
 * screen subscribes through Keyboard.addListener, and a test raises or lowers
 * it with show() and hide(). */
export const keyboard = (() => {
	const listeners = new Map<string, Set<() => void>>();
	const emit = (event: string) => {
		for (const listener of listeners.get(event) ?? []) listener();
	};
	let visible = false;
	return {
		get visible() {
			return visible;
		},
		/** How many listeners are subscribed to `event`. */
		listening(event: string) {
			return listeners.get(event)?.size ?? 0;
		},
		/** Lowers the keyboard and forgets every listener, so a mount a test
		 * left behind can't carry keyboard state into the next test. */
		reset() {
			visible = false;
			listeners.clear();
		},
		/** Sends one Keyboard event on its own, as a platform would. */
		emit,
		addListener(event: string, listener: () => void) {
			const set = listeners.get(event) ?? new Set();
			set.add(listener);
			listeners.set(event, set);
			return { remove: () => set.delete(listener) };
		},
		show() {
			visible = true;
			emit("keyboardWillShow");
			emit("keyboardDidShow");
		},
		hide() {
			visible = false;
			emit("keyboardWillHide");
			emit("keyboardDidHide");
		},
	};
})();

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
		onEndReached?: (info: { distanceFromEnd: number }) => void;
		ListFooterComponent?: ReactNode;
	}) =>
		createElement(
			"SectionList",
			null,
			props.ListHeaderComponent ?? null,
			...props.sections.flatMap((section) => [
				props.renderSectionHeader?.({ section }) ?? null,
				...section.data.map((item) => createElement("Item", { key: item.name }, props.renderItem?.({ item }) ?? null)),
			]),
			props.sections.length === 0 ? (props.ListEmptyComponent ?? null) : null,
			props.ListFooterComponent ?? null,
		);
	type ListRowInfo = { item: unknown; index: number };
	// A list's cell: VirtualizedList's CellRenderer is a PureComponent over
	// the row, its renderer and extraData. Its own cell component wraps each
	// row, as the real list does, so a test can lay a cell out (its onLayout)
	// and measure the row.
	const ListCell = memo(function ListCell(props: {
		item: unknown;
		index: number;
		renderItem?: (info: ListRowInfo) => ReactNode;
		/** Unread here: it's a prop only so a new value re-renders the cell,
		 * as the real list's extraData does. */
		extraData?: unknown;
		Cell?: ComponentType<{ item: unknown; index: number; children?: ReactNode }>;
	}) {
		const row = props.renderItem?.({ item: props.item, index: props.index }) ?? null;
		return props.Cell ? createElement(props.Cell, { item: props.item, index: props.index }, row) : row;
	});
	const FlatList = (props: {
		ref?: Ref<unknown>;
		data?: unknown[];
		keyExtractor?: (item: unknown, index: number) => string;
		renderItem?: (info: { item: unknown; index: number }) => ReactNode;
		CellRendererComponent?: ComponentType<{ item: unknown; index: number; children?: ReactNode }>;
		extraData?: unknown;
		strictMode?: boolean;
		ListHeaderComponent?: ReactNode;
		ListFooterComponent?: ReactNode;
		ListEmptyComponent?: ReactNode;
		onEndReached?: (info: { distanceFromEnd: number }) => void;
		onScrollToIndexFailed?: (info: {
			index: number;
			highestMeasuredFrameIndex: number;
			averageItemLength: number;
		}) => void;
	}) => {
		const latest = useRef(props);
		latest.current = props;
		useImperativeHandle(
			props.ref,
			() => ({
				...flatListHandle,
				// A row the real list hasn't measured makes scrollToIndex report
				// failure synchronously; flatListScrollFailures scripts how many.
				scrollToIndex: (args: { index: number }) => {
					flatListHandle.scrollToIndex(args);
					if (flatListScrollFailures.remaining <= 0) return;
					flatListScrollFailures.remaining -= 1;
					latest.current.onScrollToIndexFailed?.({
						index: args.index,
						highestMeasuredFrameIndex: -1,
						averageItemLength: 100,
					});
				},
			}),
			[],
		);
		return createElement(
			"FlatList",
			{ onEndReached: props.onEndReached },
			props.ListHeaderComponent ?? null,
			...(props.data ?? []).map((item, index) =>
				createElement(
					"Item",
					{ key: props.keyExtractor?.(item, index) ?? index },
					createElement(ListCell, {
						item,
						index,
						// As the real FlatList does: without strictMode it wraps
						// renderItem afresh on every render, so every cell re-renders
						// with the list; with it, the wrapper is memoized, and a cell
						// re-renders only for a new renderItem, row or extraData.
						renderItem: props.strictMode ? props.renderItem : (info: ListRowInfo) => props.renderItem?.(info),
						extraData: props.extraData,
						Cell: props.CellRendererComponent,
					}),
				),
			),
			(props.data ?? []).length === 0 ? (props.ListEmptyComponent ?? null) : null,
			props.ListFooterComponent ?? null,
		);
	};

	// KeyboardAvoidingView only shifts layout; the test tree renders its
	// children unchanged.
	const KeyboardAvoidingView = (props: { children?: ReactNode }) =>
		createElement("KeyboardAvoidingView", null, props.children);

	// Animated keeps its values observable: a Value holds the number the
	// screen last drove it to, and timing lands on its target at once, so a
	// test reads where a transform ended up from the rendered style.
	class AnimatedValue {
		constructor(public value: number) {}
		setValue(value: number) {
			this.value = value;
		}
	}
	const Animated = {
		View: "Animated.View",
		Value: AnimatedValue,
		timing: (value: AnimatedValue, config: { toValue: number }) => ({
			start: (done?: (result: { finished: boolean }) => void) => {
				value.setValue(config.toValue);
				done?.({ finished: true });
			},
		}),
	};

	return {
		AccessibilityInfo: {
			announceForAccessibility: vi.fn(),
			// Reduce Motion stays off and never changes here: a suite that
			// needs it mocks AccessibilityInfo itself.
			isReduceMotionEnabled: () => answered(false),
			isReduceTransparencyEnabled: () => systemGlass.read(),
			addEventListener: (event: string, listener: (value: boolean) => void) =>
				event === "reduceTransparencyChanged" ? systemGlass.listen(listener) : { remove: () => {} },
		},
		ActivityIndicator: "ActivityIndicator",
		// In front the whole test; a test that needs the app to come and go
		// mocks its own.
		AppState: { currentState: "active", addEventListener: () => ({ remove: () => {} }) },
		Animated,
		Appearance: { setColorScheme: () => {} },
		Alert: { alert: recordAlert, prompt: recordPrompt },
		FlatList,
		Image: "Image",
		Keyboard: {
			addListener: keyboard.addListener,
			dismiss: vi.fn(() => keyboard.hide()),
			isVisible: () => keyboard.visible,
		},
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

/** How many times a mocked swipeable's ref was closed; reset it per test. */
export const swipeableCalls = { closes: 0 };

/** react-native-gesture-handler/ReanimatedSwipeable as an inert host element:
 * it renders its children and carries every prop, so a test finds it by type
 * and drives its callbacks; its ref's close() is counted. */
export function gestureHandlerModuleMock() {
	const ReanimatedSwipeable = forwardRef(function ReanimatedSwipeable(
		props: { children?: ReactNode } & Record<string, unknown>,
		ref: ForwardedRef<unknown>,
	) {
		useImperativeHandle(ref, () => ({
			close: () => {
				swipeableCalls.closes += 1;
			},
			openLeft: () => {},
			openRight: () => {},
			reset: () => {},
		}));
		return createElement("ReanimatedSwipeable", props, props.children);
	});
	return {
		__esModule: true,
		default: ReanimatedSwipeable,
		SwipeDirection: { LEFT: "left", RIGHT: "right" },
	};
}

/** A Gesture.Pan() builder as a record: each setting a test reads lands in
 * `config`, and each callback in `handlers`, so a test drives the gesture's
 * end by hand. */
export interface PanGestureMock {
	config: Record<string, unknown>;
	handlers: {
		onBegin?(): void;
		onUpdate?(event: { translationX: number }): void;
		onEnd?(event: { translationX: number; velocityX: number }, success: boolean): void;
	};
}

/** react-native-gesture-handler's GestureDetector and Gesture.Pan for
 * vitest: the detector is a host element carrying its gesture, and the pan
 * a PanGestureMock whose builder methods return it. */
export function gestureDetectorModuleMock() {
	const pan = (): PanGestureMock => {
		const gesture = { config: {}, handlers: {} } as PanGestureMock;
		const setting = (name: string) => (value: unknown) => {
			gesture.config[name] = value;
			return builder;
		};
		const builder = Object.assign(gesture, {
			activeOffsetX: setting("activeOffsetX"),
			failOffsetY: setting("failOffsetY"),
			runOnJS: setting("runOnJS"),
			hitSlop: setting("hitSlop"),
			enabled: setting("enabled"),
			onBegin: (handler: PanGestureMock["handlers"]["onBegin"]) => {
				gesture.handlers.onBegin = handler;
				return builder;
			},
			onUpdate: (handler: PanGestureMock["handlers"]["onUpdate"]) => {
				gesture.handlers.onUpdate = handler;
				return builder;
			},
			onEnd: (handler: PanGestureMock["handlers"]["onEnd"]) => {
				gesture.handlers.onEnd = handler;
				return builder;
			},
		});
		return builder;
	};
	return {
		__esModule: true,
		GestureDetector: (props: { gesture: unknown; children?: ReactNode }) =>
			createElement("GestureDetector", props, props.children),
		Gesture: { Pan: pan },
	};
}

/** A finger's drag on a SwipeRow, as the row's release tracker sees it: it
 * touches down, drags through each of `via` in turn, and lets go
 * translationX points from where it began (positive to the right) at
 * velocityX. The tracker is the pan the row hands its swipeable to recognize
 * alongside. */
export function releaseSwipeRow(
	swipeable: ReactTestInstance,
	translationX: number,
	{ velocityX = 0, via = [] }: { velocityX?: number; via?: readonly number[] } = {},
) {
	const tracker = swipeable.props.simultaneousWithExternalGesture as PanGestureMock | undefined;
	if (!tracker) throw new Error("the swipeable has no release tracker");
	act(() => {
		tracker.handlers.onBegin?.();
		for (const point of [...via, translationX]) tracker.handlers.onUpdate?.({ translationX: point });
		tracker.handlers.onEnd?.({ translationX, velocityX }, true);
	});
}

/** The swipeable opening a row the way it does after a release: it says it
 * will open as the finger lets go, and that it has opened once the row
 * settles. */
export function openSwipeRow(swipeable: ReactTestInstance, direction: "left" | "right") {
	act(() => swipeable.props.onSwipeableWillOpen(direction));
	act(() => swipeable.props.onSwipeableOpen(direction));
}

/** A full swipe on a SwipeRow: the finger touches down at pageX (by default
 * well clear of the screen's left edge band), drags the row 250 points
 * (past half a 390-point window) toward `direction`, and lets go, and the
 * swipeable opens it. */
export function swipeRowFully(
	swipeable: ReactTestInstance | undefined,
	direction: "left" | "right",
	{ pageX = 200 }: { pageX?: number } = {},
) {
	if (!swipeable) throw new Error("no swipeable row");
	act(() => swipeable.findByProps({ testID: "swipe-row-content" }).props.onTouchStart({ nativeEvent: { pageX } }));
	releaseSwipeRow(swipeable, direction === "right" ? 250 : -250);
	openSwipeRow(swipeable, direction);
}

/** react-native-reanimated for vitest: Animated.ScrollView is a host
 * "ScrollView" carrying every prop (the Board's scroller, found and driven
 * as a plain one is), Animated.View a host element, and LinearTransition a
 * builder chain that returns itself, so a test can compare a view's `layout`
 * with the transition the app built. */
export function reanimatedModuleMock() {
	const transition: Record<string, () => unknown> = {};
	for (const step of ["springify", "duration", "dampingRatio"]) transition[step] = () => transition;
	return {
		__esModule: true,
		// An animated component renders as the component it wraps.
		default: {
			ScrollView: "ScrollView",
			View: "Animated.View",
			createAnimatedComponent: <T,>(component: T) => component,
		},
		LinearTransition: transition,
		// An animated style is its worklet's result at render time.
		useAnimatedStyle: <T,>(updater: () => T) => updater(),
	};
}

/** How far the keyboard has risen, 0 to 1, as react-native-keyboard-controller
 * reports it frame by frame. It follows the testkit keyboard (1 while shown),
 * unless a test holds it partway with `at`. */
export const keyboardProgress = { at: null as number | null };

/** react-native-keyboard-controller's useReanimatedKeyboardAnimation for
 * vitest: the progress above, and a render whenever the keyboard moves, since
 * a mocked shared value can't drive a style on its own. */
export function useKeyboardAnimationMock() {
	const [, rendered] = useState(0);
	useEffect(() => {
		const again = () => rendered((count) => count + 1);
		const subscriptions = [
			keyboard.addListener("keyboardWillShow", again),
			keyboard.addListener("keyboardWillHide", again),
		];
		return () => {
			for (const subscription of subscriptions) subscription.remove();
		};
	}, []);
	return {
		progress: { value: keyboardProgress.at ?? (keyboard.visible ? 1 : 0) },
		height: { value: 0 },
	};
}

/** Every scroll a mounted FlatList was asked for, oldest first: the stub's
 * ref records scrollToIndex, scrollToOffset and scrollToEnd (directly or
 * through getScrollResponder) instead of moving anything. A test clears it
 * before the mount it cares about. */
export const flatListCalls: { method: string; args?: unknown }[] = [];

/** How many of the next scrollToIndex calls fail, as they do for a row the
 * list hasn't measured: each calls the list's onScrollToIndexFailed. A test
 * sets it before the scroll it cares about and resets it after. */
export const flatListScrollFailures = { remaining: 0 };

const flatListHandle = {
	scrollToIndex: (args: unknown) => void flatListCalls.push({ method: "scrollToIndex", args }),
	scrollToOffset: (args: unknown) => void flatListCalls.push({ method: "scrollToOffset", args }),
	scrollToEnd: (args?: unknown) => void flatListCalls.push({ method: "scrollToEnd", args }),
	getScrollResponder: () => ({
		scrollToEnd: (args?: unknown) => void flatListCalls.push({ method: "scrollToEnd", args }),
	}),
};

/** One Alert.alert call the mounted tree made. */
export interface AlertRequest {
	title: string;
	message?: string;
	buttons?: { text?: string; style?: string; onPress?: () => void }[];
	options?: { cancelable?: boolean };
}

/** Every Alert.alert call the mounted tree made, oldest first. A test that
 * drives a confirmation dialog reads the buttons off the request it cares
 * about and invokes the one it wants; production Alert never returns. */
export const alertRequests: AlertRequest[] = [];

/** One Alert.prompt call the mounted tree made: a test answers it by calling
 * `callback` with the text it types, as pressing OK would. */
export interface PromptRequest {
	title: string;
	message?: string;
	callback?: (text: string) => void;
}

/** Every Alert.prompt call the mounted tree made, oldest first. */
export const promptRequests: PromptRequest[] = [];

function recordPrompt(title: string, message?: string, callback?: (text: string) => void): void {
	promptRequests.push({ title, message, callback });
}

function recordAlert(
	title: string,
	message?: string,
	buttons?: AlertRequest["buttons"],
	options?: AlertRequest["options"],
): void {
	alertRequests.push({ title, message, buttons, options });
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

/** What expo-haptics played, in order ("selection", "impact:light",
 * "notification:warning"...), recorded by the setup file's fake
 * (vitestSetup.ts). Clear it (`playedHaptics.length = 0`) before the press
 * a test asserts on. */
export const playedHaptics: string[] = [];

/** The connection value the retained-screen suites report through their
 * mocked ConnectionProvider: the Work hub's profile, its client, the state
 * the test drives, and the fatal flag the retained-screen wiring reads. One literal where five suites' fixtures matched field for
 * field (#1942), so the shape the screens read cannot drift between suites -
 * the test-side twin of the useRetainedScreenConnection wiring the screens
 * themselves share (#2164). A test whose scenario needs a different hub,
 * retry or verdict spreads its override over the result, so the outlier
 * stays visible at its use.
 *
 * The status clock (ConnectionClock) reads as the provider's would at the
 * moment the connection reaches `state`: a connection that isn't live went
 * down, and was last live, just now. */
export function screenConnection(client: unknown, state: ConnectionState): Record<string, unknown> {
	const downAt = state === "ready" ? null : Date.now();
	return {
		activeProfile: { id: "hub-1", name: "Work hub" },
		profiles: [{ id: "hub-1", name: "Work hub", origin: "https://hub.example" }],
		client,
		state,
		fatal: false,
		downSince: downAt,
		lastLiveAt: downAt,
	};
}

/** `connection` after it has been down long enough for the status to say so
 * (spec 14: "Reconnecting…" from 2 seconds), keeping its client and hub. */
export function dropped(
	connection: Record<string, unknown>,
	state: ConnectionState = "reconnecting",
	downFor = 2_000,
): Record<string, unknown> {
	const downAt = Date.now() - downFor;
	return { ...connection, state, downSince: downAt, lastLiveAt: downAt };
}

// The trees the running test has mounted, newest last.
const mountedThisTest: ReactTestRenderer[] = [];

/** Unmounts every tree the running test mounted, newest first; a tree a test
 * already unmounted is a no-op. A suite whose afterEach cleans up anything a
 * mounted tree may still use (a sheet host's release, a runtime's stop, a
 * flow's dispose, restored mocks or real timers) calls this first, so that
 * cleanup never runs under a live tree. */
export function unmountMountedTrees(): void {
	for (const tree of mountedThisTest.splice(0).reverse()) act(() => tree.unmount());
}

/** Mounts `element` and flushes its effects, returning the test renderer.
 * `options.createNodeMock` hands host components' refs a stand-in, such as a
 * ScrollView whose scrollTo a test records.
 *
 * The tree is unmounted when the test that mounted it ends (unmountMountedTrees
 * runs from onTestFinished, after the suite's afterEach hooks, unless an
 * afterEach ran it first). A tree left mounted keeps its timers and
 * subscriptions running, and their updates land after the file's last test,
 * outside act: React's warning about them can reach the console while vitest
 * tears the worker down, which fails the run (#3916). Call render only inside
 * a test or a beforeEach. */
export function render(element: ReactElement, options?: TestRendererOptions): ReactTestRenderer {
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(element, options);
	});
	if (mountedThisTest.length === 0) onTestFinished(unmountMountedTrees);
	mountedThisTest.push(tree);
	return tree;
}

/** Lets the mounted trees' pending work land inside act: one timer turn,
 * which also runs every microtask queued ahead of it (a fake hub's answer,
 * the store updates it starts). Under fake timers it never resolves:
 * advance the timers instead. */
export async function settle(): Promise<void> {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
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
	const tree = render(createElement(Probe));
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

/** The text one node reads as: its strings and its descendants', joined with
 * nothing between them, the way nested Text elements run together on screen. */
export function textOf(node: ReactTestInstance): string {
	return node.children.map((child) => (typeof child === "string" ? child : textOf(child))).join("");
}

/** The first mounted Pressable VoiceOver names `label`: its accessibility
 * label, or, with none, the text inside it, which iOS reads in its place;
 * undefined when there is none. */
export function pressable(tree: ReactTestRenderer, label: string): ReactTestInstance | undefined {
	return tree.root.findAll(
		(node) => String(node.type) === "Pressable" && (node.props.accessibilityLabel ?? textOf(node)) === label,
	)[0];
}

/** The one scrolling body of the dock whose card carries `testID` (spec 8.4),
 * checked to shrink with its card; `holds` says whether the Pressable
 * labelled `label` scrolls inside it or stays put outside it. */
export function dockBody(tree: ReactTestRenderer, testID: string) {
	const card = tree.root.findByProps({ testID });
	expect(card.props.style).toMatchObject({ flexShrink: 1 });
	const [scroller, ...others] = card.findAll((node) => String(node.type) === "ScrollView");
	if (!scroller) throw new Error(`the dock ${testID} has no scroller`);
	expect(others).toHaveLength(0);
	// No floor: the answer controls outside it win whatever room is short.
	expect(scroller.props.style).toEqual(shrinkingScroller);
	return {
		scroller,
		holds(label: string) {
			const target = pressable(tree, label);
			if (!target) throw new Error(`no pressable labelled ${label}`);
			return scroller.findAll((node) => node === target).length > 0;
		},
	};
}

/** The composer's focus as the Composer would report it: `focused` or not. */
export function composerFocusedAs(focused: boolean): ComposerFocus {
	const focus = new ComposerFocus();
	focus.set(focused);
	return focus;
}
