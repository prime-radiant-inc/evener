import { memo, useEffect, useRef, useState } from "react";
import { StyleSheet, Text, View, type AccessibilityActionEvent, type AccessibilityActionInfo } from "react-native";
import { WebView, type WebViewMessageEvent } from "react-native-webview";
import { MERMAID_PAGE_HTML } from "./generated/mermaidPage";
import { useColors } from "./ui";

// Last posted height per source, so a FlatList remount shows the right-sized
// placeholder while the WebView re-initializes (spec: Known trade-offs).
const heightCache = new Map<string, number>();

const PLACEHOLDER_HEIGHT = 120;

type PageMessage = { type: "ready" } | { type: "height"; value: number } | { type: "error"; message: string };

function parsePageMessage(raw: string): PageMessage | null {
	try {
		const message: unknown = JSON.parse(raw);
		if (typeof message !== "object" || message === null || !("type" in message)) return null;
		if (message.type === "ready") return { type: "ready" };
		if (message.type === "height" && "value" in message && typeof message.value === "number") {
			return { type: "height", value: message.value };
		}
		if (message.type === "error" && "message" in message && typeof message.message === "string") {
			return { type: "error", message: message.message };
		}
		return null;
	} catch {
		return null;
	}
}

// Maps the app palette onto the same mermaid themeVariables the web widget sets
// (cmd/evener-hub/frontend/src/widgets/mermaid/resolveScheme.ts reads
// --surface-canvas/--surface-1/--edge/--ink-hi/--ink-mid/--font-sans), so a
// native diagram carries the app's colors instead of mermaid's own default
// theme. The app has no sans token: its UI text is the platform system face,
// so the diagram gets the same stack the WebView would default to on its own.
function mermaidTheme(colors: ReturnType<typeof useColors>): Record<string, string> {
	const { palette } = colors;
	return {
		dark: String(palette.scheme === "dark"),
		background: palette.canvas,
		primaryColor: palette.surface,
		primaryBorderColor: palette.edge,
		primaryTextColor: palette.inkHi,
		lineColor: palette.inkMid,
		fontFamily: "system-ui, sans-serif",
	};
}

export const MermaidDiagram = memo(function MermaidDiagram({
	source,
	accessibilityActions,
	onAccessibilityAction,
}: {
	source: string;
	accessibilityActions?: AccessibilityActionInfo[];
	onAccessibilityAction?: (event: AccessibilityActionEvent) => void;
}) {
	const colors = useColors();
	const webView = useRef<WebView>(null);
	const [height, setHeight] = useState<number | null>(heightCache.get(source) ?? null);
	const [failed, setFailed] = useState(false);

	// The page renders under one fixed element id, so two render messages in
	// flight would race. renderInFlight guards the post; renderQueued coalesces
	// at most one change that arrived while waiting for the page's reply.
	const pageReady = useRef(false);
	const renderInFlight = useRef(false);
	const renderQueued = useRef(false);

	// The page's bootstrap answers a render message; the source and theme cross
	// as JSON, never string-interpolated into JS (spec: Security).
	function postRender() {
		if (!pageReady.current) return;
		if (renderInFlight.current) {
			renderQueued.current = true;
			return;
		}
		const view = webView.current;
		if (!view) return;
		renderInFlight.current = true;
		view.postMessage(JSON.stringify({ type: "render", source, theme: mermaidTheme(colors), mode: "fit" }));
	}

	// A render settled: release the one change queued while it was in flight.
	function settleRender() {
		renderInFlight.current = false;
		if (renderQueued.current) {
			renderQueued.current = false;
			postRender();
		}
	}

	function handleMessage(event: WebViewMessageEvent) {
		const message = parsePageMessage(event.nativeEvent.data);
		if (message === null) return;
		if (message.type === "ready") {
			// A fresh page has nothing outstanding from before its listener
			// attached, so reset and render the current source.
			pageReady.current = true;
			renderInFlight.current = false;
			renderQueued.current = false;
			postRender();
			return;
		}
		if (message.type === "height") {
			heightCache.set(source, message.value);
			setHeight(message.value);
			settleRender();
			return;
		}
		setFailed(true);
		settleRender();
	}

	// biome-ignore lint/correctness/useExhaustiveDependencies: keyed on the palette, same discipline as MarkdownResponse
	useEffect(postRender, [colors.palette, source]);

	if (failed) {
		return (
			<View
				accessible={true}
				accessibilityLabel="Diagram source"
				accessibilityActions={accessibilityActions}
				onAccessibilityAction={onAccessibilityAction}
				style={[styles.fallback, { backgroundColor: colors.surface, borderColor: colors.border }]}
			>
				<Text style={[styles.fallbackSource, { color: colors.text }]}>{source}</Text>
				<Text style={[styles.fallbackNote, { color: colors.secondary }]}>Couldn't render this diagram.</Text>
			</View>
		);
	}
	return (
		<View
			accessible={true}
			accessibilityLabel="Diagram"
			accessibilityActions={accessibilityActions}
			onAccessibilityAction={onAccessibilityAction}
			style={{ height: height ?? PLACEHOLDER_HEIGHT }}
		>
			<WebView
				ref={webView}
				originWhitelist={["about:blank"]}
				source={{ html: MERMAID_PAGE_HTML }}
				onShouldStartLoadWithRequest={(request) => request.url === "about:blank"}
				scrollEnabled={false}
				style={styles.webView}
				onMessage={handleMessage}
			/>
		</View>
	);
});

const styles = StyleSheet.create({
	webView: { flex: 1, backgroundColor: "transparent" },
	fallback: {
		borderWidth: 1,
		borderRadius: 10,
		padding: 12,
		gap: 6,
	},
	fallbackSource: {
		fontFamily: "Menlo",
		fontSize: 13,
		lineHeight: 18,
	},
	fallbackNote: {
		fontSize: 13,
	},
});
