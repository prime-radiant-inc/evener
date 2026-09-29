import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { MermaidDiagram } from "./MermaidDiagram";
import { render, renderedText } from "./renderNative.testkit";

const mode = vi.hoisted(() => ({ scheme: "light" as "light" | "dark" }));
// The repo's native-module seam: react-native's surface becomes inert host
// elements, and the WebView a host string, so a test reads OUR wiring (props,
// message handling, fallback state) rather than the native view itself.
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	useColorScheme: () => mode.scheme,
}));
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));

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
	expect(node.props.accessibilityActions).toBe(actions);
});
