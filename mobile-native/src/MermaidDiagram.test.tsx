import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { fonts } from "./design/tokens";
import { MermaidDiagram } from "./MermaidDiagram";
import { render, renderedText, textOf } from "./renderNative.testkit";

const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
const clipboard = vi.hoisted(() => ({ setStringAsync: vi.fn() }));
// A mutable Platform, so a test can read the Android font branch the way the
// mock's mutable color scheme drives the palette.
const platform = vi.hoisted(() => ({ OS: "ios" as "ios" | "android" }));
// The repo's native-module seam: react-native's surface becomes inert host
// elements, and the WebView a host string, so a test reads OUR wiring (props,
// message handling, fallback state) rather than the native view itself.
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	Platform: platform,
	useColorScheme: () => mode.scheme,
}));
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-clipboard", () => clipboard);

/** A wrapper found the way VoiceOver finds it, by its accessibility label. */
function wrapper(tree: ReactTestRenderer, label: string) {
	return tree.root.findByProps({ accessibilityLabel: label });
}

it("locks the WebView down", () => {
	const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
	const webview = tree.root.findByType("WebView" as never);
	expect(webview.props.originWhitelist).toEqual(["about:blank"]);
	expect(webview.props.onShouldStartLoadWithRequest({ url: "about:blank" })).toBe(true);
	expect(webview.props.onShouldStartLoadWithRequest({ url: "https://evil.example" })).toBe(false);
});

it("posts the source as JSON once the page reports ready", () => {
	const postMessage = vi.fn();
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(<MermaidDiagram source="graph TD; A-->B" />, {
			createNodeMock: (element) => (element.type === ("WebView" as never) ? { postMessage } : {}),
		});
	});
	// Nothing is posted before the page's listener is attached.
	expect(postMessage).not.toHaveBeenCalled();
	act(() => {
		tree.root
			.findByType("WebView" as never)
			.props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "ready" }) } });
	});
	expect(postMessage).toHaveBeenCalledTimes(1);
	expect(JSON.parse(postMessage.mock.calls[0]?.[0] as string)).toMatchObject({
		type: "render",
		source: "graph TD; A-->B",
		mode: "fit",
	});
});

it("sizes itself from the page's height message", () => {
	const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
	const webview = tree.root.findByType("WebView" as never);
	act(() => {
		webview.props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "height", value: 213 }) } });
	});
	expect(wrapper(tree, "Diagram").props.style.height).toBe(213);
});

it("serializes render posts: a change while one is in flight waits for the reply", () => {
	const postMessage = vi.fn();
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(<MermaidDiagram source="graph TD; A-->B" />, {
			createNodeMock: (element) => (element.type === ("WebView" as never) ? { postMessage } : {}),
		});
	});
	const webview = () => tree.root.findByType("WebView" as never);
	act(() => {
		webview().props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "ready" }) } });
	});
	expect(postMessage).toHaveBeenCalledTimes(1);
	// The source changes while the first render is still outstanding: the new
	// render must queue, because the page renders under one fixed element id.
	act(() => {
		tree.update(<MermaidDiagram source="graph TD; A-->C" />);
	});
	expect(postMessage).toHaveBeenCalledTimes(1);
	// The first render's height reply releases exactly one queued render.
	act(() => {
		webview().props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "height", value: 200 }) } });
	});
	expect(postMessage).toHaveBeenCalledTimes(2);
	expect(JSON.parse(postMessage.mock.calls[1]?.[0] as string).source).toBe("graph TD; A-->C");
});

it("falls back to a code view on an error message", () => {
	const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
	const webview = tree.root.findByType("WebView" as never);
	act(() => {
		webview.props.onMessage({
			nativeEvent: { data: JSON.stringify({ type: "error", message: "Parse error" }) },
		});
	});
	expect(tree.root.findAllByType("WebView" as never)).toHaveLength(0);
	expect(renderedText(tree)).toContain("graph TD; A-->B");
	expect(renderedText(tree)).toContain("Couldn't render this diagram.");
});

it("ignores malformed page messages", () => {
	// A source no earlier test has measured, so the placeholder height is the
	// uncached default rather than another test's cached measurement.
	const tree = render(<MermaidDiagram source="graph TD; M-->N" />);
	const webview = tree.root.findByType("WebView" as never);
	act(() => {
		webview.props.onMessage({ nativeEvent: { data: "not json" } });
		webview.props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "height" }) } });
	});
	// Still a live diagram, not the error fallback.
	expect(tree.root.findAllByType("WebView" as never)).toHaveLength(1);
	expect(wrapper(tree, "Diagram").props.style.height).toBe(120);
});

it("carries accessibility actions on its own accessible wrapper", () => {
	const actions = [{ name: "select", label: "Select text" }];
	const tree = render(<MermaidDiagram source="x" accessibilityActions={actions} onAccessibilityAction={() => {}} />);
	const node = wrapper(tree, "Diagram");
	expect(node.props.accessible).toBe(true);
	// The caller's actions ride along; the appended "open" action is the only
	// way a screen-reader user (the tap Pressable is accessible={false}) can
	// open the viewer.
	expect(node.props.accessibilityActions).toEqual([...actions, { name: "open", label: "Open fullscreen" }]);
});

it("opens the viewer from the wrapper's open action and forwards the caller's actions", () => {
	const onAction = vi.fn();
	const actions = [{ name: "select", label: "Select text" }];
	const tree = render(
		<MermaidDiagram source="graph TD; A-->B" accessibilityActions={actions} onAccessibilityAction={onAction} />,
	);
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
	const node = wrapper(tree, "Diagram");
	act(() => {
		node.props.onAccessibilityAction({ nativeEvent: { actionName: "open" } });
	});
	// The "open" action opens the fullscreen viewer.
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(1);
	// A caller's own action still reaches its handler untouched.
	act(() => {
		node.props.onAccessibilityAction({ nativeEvent: { actionName: "select" } });
	});
	expect(onAction).toHaveBeenCalledTimes(1);
	expect(onAction.mock.calls[0]?.[0].nativeEvent.actionName).toBe("select");
});

it("opens a fullscreen viewer on press, with source toggle and copy", () => {
	clipboard.setStringAsync.mockClear();
	const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
	// Closed by default: no modal, only the inline WebView.
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);

	act(() => {
		tree.root.findByProps({ testID: "mermaid-open" }).props.onPress();
	});
	// The modal opens full-screen hosting a second WebView.
	const modal = tree.root.findByType("Modal" as never);
	expect(modal.props.presentationStyle).toBe("fullScreen");
	expect(modal.props.animationType).toBe("slide");
	expect(tree.root.findAllByType("WebView" as never)).toHaveLength(2);

	// "Show source" swaps the WebView for the source text.
	act(() => {
		tree.root.findByProps({ accessibilityLabel: "Show source" }).props.onPress();
	});
	expect(renderedText(tree)).toContain("graph TD; A-->B");
	expect(tree.root.findAllByType("WebView" as never)).toHaveLength(1);

	// "Copy source" runs the clipboard.
	act(() => {
		tree.root.findByProps({ accessibilityLabel: "Copy source" }).props.onPress();
	});
	expect(clipboard.setStringAsync).toHaveBeenCalledWith("graph TD; A-->B");

	// "Done" closes the modal.
	act(() => {
		tree.root.findByProps({ accessibilityLabel: "Done" }).props.onPress();
	});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
});

it("gives the fullscreen WebView the same lockdown and posts mode:zoom", () => {
	const postMessage = vi.fn();
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(<MermaidDiagram source="graph TD; A-->B" />, {
			createNodeMock: (element) => (element.type === ("WebView" as never) ? { postMessage } : {}),
		});
	});
	act(() => {
		tree.root.findByProps({ testID: "mermaid-open" }).props.onPress();
	});
	const webviews = tree.root.findAllByType("WebView" as never);
	expect(webviews).toHaveLength(2);
	const fullscreen = webviews[1];
	expect(fullscreen.props.originWhitelist).toEqual(["about:blank"]);
	expect(fullscreen.props.onShouldStartLoadWithRequest({ url: "about:blank" })).toBe(true);
	expect(fullscreen.props.onShouldStartLoadWithRequest({ url: "https://evil.example" })).toBe(false);
	expect(fullscreen.props.scrollEnabled).toBe(true);

	act(() => {
		fullscreen.props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "ready" }) } });
	});
	expect(JSON.parse(postMessage.mock.calls.at(-1)?.[0] as string)).toMatchObject({
		type: "render",
		source: "graph TD; A-->B",
		mode: "zoom",
	});
});

it("routes touches through a pointerEvents=none layer to the opener", () => {
	const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
	const webview = tree.root.findByType("WebView" as never);
	// On device the WebView would swallow the tap; the immediate parent disables
	// its pointer events so the touch reaches the Pressable.
	const layer = webview.parent;
	expect(layer?.props.pointerEvents).toBe("none");
	let node = layer;
	let pressable: typeof node = null;
	while (node) {
		if (String(node.type) === "Pressable" && node.props.testID === "mermaid-open") {
			pressable = node;
			break;
		}
		node = node.parent;
	}
	expect(pressable).not.toBeNull();
	// The touch routed to the Pressable still opens the viewer.
	act(() => {
		expect(pressable).not.toBeNull();
		pressable?.props.onPress();
	});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(1);
});

it("re-posts the zoom render when the palette changes", () => {
	clipboard.setStringAsync.mockClear();
	mode.scheme = "light";
	const postMessage = vi.fn();
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(<MermaidDiagram source="graph TD; A-->B" />, {
			createNodeMock: (element) => (element.type === ("WebView" as never) ? { postMessage } : {}),
		});
	});
	act(() => {
		tree.root.findByProps({ testID: "mermaid-open" }).props.onPress();
	});
	act(() => {
		for (const view of tree.root.findAllByType("WebView" as never)) {
			view.props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "ready" }) } });
		}
	});
	const zoomCalls = () =>
		postMessage.mock.calls.map((call) => JSON.parse(call[0] as string)).filter((msg) => msg.mode === "zoom");
	expect(zoomCalls()).toHaveLength(1);
	// The zoom render is now serialized: let its height reply settle before the
	// palette changes, so this case pins the immediate re-post when idle.
	act(() => {
		tree.root
			.findAllByType("WebView" as never)[1]
			.props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "height", value: 300 }) } });
	});

	mode.scheme = "dark";
	act(() => {
		// A fresh callback identity busts the component's memo, so it re-reads
		// useColorScheme the way a real appearance subscription would.
		tree.update(<MermaidDiagram source="graph TD; A-->B" onAccessibilityAction={() => {}} />);
	});
	expect(zoomCalls()).toHaveLength(2);
	expect(zoomCalls().at(-1)?.theme.dark).toBe("true");
	mode.scheme = "light";
});

it("queues a palette flip during an in-flight zoom render and re-posts it on the reply", () => {
	mode.scheme = "light";
	const postMessage = vi.fn();
	let tree!: ReactTestRenderer;
	act(() => {
		tree = create(<MermaidDiagram source="graph TD; A-->B" onAccessibilityAction={() => {}} />, {
			createNodeMock: (element) => (element.type === ("WebView" as never) ? { postMessage } : {}),
		});
	});
	act(() => {
		tree.root.findByProps({ testID: "mermaid-open" }).props.onPress();
	});
	const webviews = () => tree.root.findAllByType("WebView" as never);
	act(() => {
		for (const view of webviews()) {
			view.props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "ready" }) } });
		}
	});
	const zoomCalls = () =>
		postMessage.mock.calls.map((call) => JSON.parse(call[0] as string)).filter((msg) => msg.mode === "zoom");
	expect(zoomCalls()).toHaveLength(1);
	// A palette flip while the zoom render is still outstanding must queue, not
	// race the page's fixed element id with a second render.
	mode.scheme = "dark";
	act(() => {
		tree.update(<MermaidDiagram source="graph TD; A-->B" onAccessibilityAction={() => {}} />);
	});
	expect(zoomCalls()).toHaveLength(1);
	// The zoom page's height reply releases exactly one queued render.
	act(() => {
		webviews()[1].props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "height", value: 300 }) } });
	});
	expect(zoomCalls()).toHaveLength(2);
	expect(zoomCalls().at(-1)?.theme.dark).toBe("true");
	mode.scheme = "light";
});

it("uses the platform mono face, not a bare Menlo, for the source and fallback views", () => {
	try {
		platform.OS = "android";
		const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
		// The error fallback shows the source in the platform monospace face.
		act(() => {
			tree.root.findByType("WebView" as never).props.onMessage({
				nativeEvent: { data: JSON.stringify({ type: "error", message: "Parse error" }) },
			});
		});
		const fallback = tree.root.findAllByType("Text" as never).find((node) => textOf(node) === "graph TD; A-->B");
		expect(fallback?.props.style).toContainEqual(expect.objectContaining({ fontFamily: "monospace" }));

		// The fullscreen source view carries the same face.
		const viewer = render(<MermaidDiagram source="graph TD; A-->B" />);
		act(() => {
			viewer.root.findByProps({ testID: "mermaid-open" }).props.onPress();
		});
		act(() => {
			viewer.root.findByProps({ accessibilityLabel: "Show source" }).props.onPress();
		});
		const sourceText = viewer.root.findAllByType("Text" as never).find((node) => textOf(node) === "graph TD; A-->B");
		expect(sourceText?.props.style).toContainEqual(expect.objectContaining({ fontFamily: "monospace" }));

		// iOS keeps the app's Mono token.
		platform.OS = "ios";
		const iosTree = render(<MermaidDiagram source="graph TD; A-->B" />);
		act(() => {
			iosTree.root.findByType("WebView" as never).props.onMessage({
				nativeEvent: { data: JSON.stringify({ type: "error", message: "Parse error" }) },
			});
		});
		const iosFallback = iosTree.root.findAllByType("Text" as never).find((node) => textOf(node) === "graph TD; A-->B");
		expect(iosFallback?.props.style).toContainEqual(expect.objectContaining({ fontFamily: fonts.mono }));
	} finally {
		platform.OS = "ios";
	}
});

it("does not open the viewer from the error fallback", () => {
	const tree = render(<MermaidDiagram source="graph TD; A-->B" />);
	act(() => {
		tree.root.findByType("WebView" as never).props.onMessage({
			nativeEvent: { data: JSON.stringify({ type: "error", message: "Parse error" }) },
		});
	});
	// The fallback is plain text with no tap target, so nothing can open a modal.
	expect(tree.root.findAllByProps({ testID: "mermaid-open" })).toHaveLength(0);
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
});

it("clears the error fallback when the source changes to a valid diagram", () => {
	// An invalid source settles into the error fallback...
	const tree = render(<MermaidDiagram source={"graph TD; A[unclosed"} />);
	act(() => {
		tree.root.findByType("WebView" as never).props.onMessage({
			nativeEvent: { data: JSON.stringify({ type: "error", message: "Parse error" }) },
		});
	});
	expect(tree.root.findAllByType("WebView" as never)).toHaveLength(0);
	// ...and an edit to a valid source must bring the diagram back, not keep the
	// fallback for the lifetime of the mounted row.
	act(() => {
		tree.update(<MermaidDiagram source={"graph TD; A-->B"} />);
	});
	expect(tree.root.findAllByType("WebView" as never)).toHaveLength(1);
});
