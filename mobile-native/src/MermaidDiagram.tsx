import { memo, useEffect, useRef, useState, type RefObject } from "react";
import {
	Pressable,
	ScrollView,
	StyleSheet,
	Text,
	View,
	type AccessibilityActionEvent,
	type AccessibilityActionInfo,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { WebView, type WebViewMessageEvent } from "react-native-webview";
import { HoldingModal } from "./alerts/HoldingModal";
import { copyText } from "./clipboard";
import { MERMAID_PAGE_HTML } from "./generated/mermaidPage";
import { codeFontFamily } from "./markdownStyle";
import { Action, styles as uiStyles, useColors } from "./ui";

// Last posted height per source, so a FlatList remount shows the right-sized
// placeholder while the WebView re-initializes (spec: Known trade-offs).
const heightCache = new Map<string, number>();

const PLACEHOLDER_HEIGHT = 120;

// The VoiceOver/TalkBack rotor action that opens the fullscreen viewer. The
// tap-to-open Pressable is accessible={false}, so this action on the diagram's
// accessible wrapper is the only way a screen-reader user can open the viewer.
const OPEN_ACTION: AccessibilityActionInfo = { name: "open", label: "Open fullscreen" };

// The WebView lockdown shared by the inline and zoom views; only scrollEnabled
// differs (the inline diagram must not scroll, the fullscreen viewer may). One
// object so the two cannot drift.
const MERMAID_WEBVIEW_PROPS = {
	originWhitelist: ["about:blank"],
	source: { html: MERMAID_PAGE_HTML },
	onShouldStartLoadWithRequest: (request: { url: string }) => request.url === "about:blank",
};

type PageMessage =
	| { type: "ready" }
	| { type: "height"; value: number; id?: number }
	| { type: "error"; message: string; id?: number };

type RenderPost = { type: "render"; source: string; theme: Record<string, string>; mode: "fit" | "zoom" };
type PostedRender = RenderPost & { id: number };

// The inline and zoom WebViews render under one fixed element id inside one
// page, so two render messages in flight would race: a theme flip mid-render
// could land as an id collision. This hook serializes posts for one WebView -
// a post while one is in flight queues at most one pending message, re-posted
// only after the page's reply. Both WebViews share it so the discipline is
// written once.
function useSerializedRenderPosts(webView: RefObject<WebView | null>) {
	const pageReady = useRef(false);
	const renderInFlight = useRef(false);
	const renderQueued = useRef(false);
	// The most recent request, so a queued post re-sends the latest source/theme.
	const pending = useRef<PostedRender | null>(null);
	// Every posted render is stamped with the next id at POST time (a queued one
	// included), so the latest id always names the newest REQUESTED render. A
	// reply carrying an older id belongs to a source that has since been
	// superseded, and must not be applied to the current one.
	const nextId = useRef(0);
	const latestId = useRef(0);

	function send(message: PostedRender) {
		const view = webView.current;
		if (!view) return;
		renderInFlight.current = true;
		view.postMessage(JSON.stringify(message));
	}

	function post(message: RenderPost) {
		const id = nextId.current + 1;
		nextId.current = id;
		latestId.current = id;
		const stamped: PostedRender = { ...message, id };
		pending.current = stamped;
		if (!pageReady.current) return;
		if (renderInFlight.current) {
			renderQueued.current = true;
			return;
		}
		send(stamped);
	}

	// The page reports ready: a fresh page has nothing outstanding from before
	// its listener attached, so reset the flags before the caller posts.
	function markReady() {
		pageReady.current = true;
		renderInFlight.current = false;
		renderQueued.current = false;
	}

	// A render settled: release the one change queued while it was in flight.
	function settle() {
		renderInFlight.current = false;
		if (renderQueued.current && pending.current) {
			renderQueued.current = false;
			send(pending.current);
		}
	}

	return { post, markReady, settle, latestId: () => latestId.current };
}

function parsePageMessage(raw: string): PageMessage | null {
	try {
		const message: unknown = JSON.parse(raw);
		if (typeof message !== "object" || message === null || !("type" in message)) return null;
		if (message.type === "ready") return { type: "ready" };
		if (message.type === "height" && "value" in message && typeof message.value === "number") {
			return {
				type: "height",
				value: message.value,
				id: "id" in message && typeof message.id === "number" ? message.id : undefined,
			};
		}
		if (message.type === "error" && "message" in message && typeof message.message === "string") {
			return {
				type: "error",
				message: message.message,
				id: "id" in message && typeof message.id === "number" ? message.id : undefined,
			};
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

// The fullscreen viewer's WebView: the same page and lockdown as the inline
// one, but sized to fill the modal and posted mode:"zoom" so the page scales
// the diagram to the taller viewport instead of measuring a fit height.
function ZoomWebView({ source }: { source: string }) {
	const colors = useColors();
	const webView = useRef<WebView>(null);
	const renderPosts = useSerializedRenderPosts(webView);

	function handleMessage(event: WebViewMessageEvent) {
		const message = parsePageMessage(event.nativeEvent.data);
		if (message === null) return;
		if (message.type === "ready") {
			renderPosts.markReady();
			renderPosts.post({ type: "render", source, theme: mermaidTheme(colors), mode: "zoom" });
			return;
		}
		// A height or error reply settles the in-flight render, releasing any
		// change queued while it ran.
		renderPosts.settle();
	}

	// biome-ignore lint/correctness/useExhaustiveDependencies: keyed on the palette, same discipline as the inline view
	useEffect(() => {
		renderPosts.post({ type: "render", source, theme: mermaidTheme(colors), mode: "zoom" });
	}, [colors.palette, source]);

	return (
		<WebView
			ref={webView}
			{...MERMAID_WEBVIEW_PROPS}
			scrollEnabled={true}
			style={styles.webView}
			onMessage={handleMessage}
		/>
	);
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
	const [open, setOpen] = useState(false);
	const [showSource, setShowSource] = useState(false);

	// The page's bootstrap answers a render message; the source and theme cross
	// as JSON, never string-interpolated into JS (spec: Security). The shared
	// hook serializes posts so a palette flip mid-render cannot race.
	const renderPosts = useSerializedRenderPosts(webView);

	function handleMessage(event: WebViewMessageEvent) {
		const message = parsePageMessage(event.nativeEvent.data);
		if (message === null) return;
		if (message.type === "ready") {
			// A fresh page has nothing outstanding from before its listener
			// attached, so reset and render the current source.
			renderPosts.markReady();
			renderPosts.post({ type: "render", source, theme: mermaidTheme(colors), mode: "fit" });
			return;
		}
		if (message.type === "height") {
			// Only the newest render's height is valid for the current source: a
			// reply for a source that has since been superseded must not be stored
			// here. Its arrival still settles the queue below.
			if (message.id === renderPosts.latestId()) {
				heightCache.set(source, message.value);
				setHeight(message.value);
			}
			renderPosts.settle();
			return;
		}
		// Only the newest render's error may fail the current source: a superseded
		// render's late error would unmount the WebView that the queued, valid
		// render still needs. An id-less page reply (no echo) is still honored.
		if (message.id === undefined || message.id === renderPosts.latestId()) {
			setFailed(true);
		}
		renderPosts.settle();
	}

	// biome-ignore lint/correctness/useExhaustiveDependencies: keyed on the palette, same discipline as MarkdownResponse
	useEffect(() => {
		// A new source (or a theme flip) gets a fresh render: drop any earlier
		// failure so the WebView returns, and re-seed the placeholder from the
		// cache for THIS source instead of the previous row's measured height.
		setFailed(false);
		setHeight(heightCache.get(source) ?? null);
		renderPosts.post({ type: "render", source, theme: mermaidTheme(colors), mode: "fit" });
	}, [colors.palette, source]);

	// Closing the viewer resets the source toggle, so a reopen starts on the
	// diagram rather than wherever the last visit left it.
	function close() {
		setOpen(false);
		setShowSource(false);
	}

	// The caller's accessibility actions must keep reaching their handler; the
	// appended "open" action opens the viewer instead. The Pressable below is
	// accessible={false}, so this wrapper is the element VoiceOver/TalkBack
	// focuses, and without this action there is no way to open the viewer.
	function handleAccessibilityAction(event: AccessibilityActionEvent) {
		if (event.nativeEvent.actionName === OPEN_ACTION.name) {
			setOpen(true);
			return;
		}
		onAccessibilityAction?.(event);
	}

	if (failed) {
		return (
			<View
				accessible={true}
				accessibilityLabel="Diagram source"
				accessibilityActions={accessibilityActions}
				onAccessibilityAction={onAccessibilityAction}
				style={[styles.fallback, { backgroundColor: colors.surface, borderColor: colors.border }]}
			>
				<Text style={[styles.fallbackSource, { color: colors.text, fontFamily: codeFontFamily() }]}>{source}</Text>
				<Text style={[styles.fallbackNote, { color: colors.secondary }]}>Couldn't render this diagram.</Text>
			</View>
		);
	}
	return (
		<>
			{/* On device the WebView is the native touch responder over its
				region, so a tap would never reach the Pressable underneath. The
				diagram offers no interaction of its own (scrollEnabled=false, no
				surviving anchors), so a pointerEvents="none" layer hands every
				touch in the region to the Pressable's tap-to-open. VoiceOver still
				keeps the inner accessible View as its element. */}
			<Pressable accessible={false} testID="mermaid-open" onPress={() => setOpen(true)}>
				<View
					accessible={true}
					accessibilityLabel="Diagram"
					accessibilityActions={[...(accessibilityActions ?? []), OPEN_ACTION]}
					onAccessibilityAction={handleAccessibilityAction}
					style={{ height: height ?? PLACEHOLDER_HEIGHT }}
				>
					<View pointerEvents="none" style={styles.fill}>
						<WebView
							ref={webView}
							{...MERMAID_WEBVIEW_PROPS}
							scrollEnabled={false}
							style={styles.webView}
							onMessage={handleMessage}
						/>
					</View>
				</View>
			</Pressable>
			{open ? (
				<HoldingModal visible={true} animationType="slide" presentationStyle="fullScreen" onRequestClose={close}>
					<SafeAreaView style={[styles.fill, { backgroundColor: colors.background }]}>
						<View style={styles.header}>
							<Action onPress={() => setShowSource((showing) => !showing)}>
								{showSource ? "Show diagram" : "Show source"}
							</Action>
							<View style={styles.headerRight}>
								<Action onPress={() => void copyText(source)}>Copy source</Action>
								<Action onPress={close}>Done</Action>
							</View>
						</View>
						{showSource ? (
							<ScrollView contentContainerStyle={styles.sourceContainer}>
								<Text style={[styles.source, { color: colors.text, fontFamily: codeFontFamily() }]}>{source}</Text>
							</ScrollView>
						) : (
							<ZoomWebView source={source} />
						)}
					</SafeAreaView>
				</HoldingModal>
			) : null}
		</>
	);
});

const styles = StyleSheet.create({
	fill: { flex: 1 },
	webView: { flex: 1, backgroundColor: "transparent" },
	header: {
		...uiStyles.row,
		paddingHorizontal: 16,
		paddingVertical: 8,
	},
	headerRight: {
		flexDirection: "row",
		alignItems: "center",
		gap: 12,
	},
	sourceContainer: { padding: 16 },
	source: {
		fontSize: 13,
		lineHeight: 18,
	},
	fallback: {
		borderWidth: 1,
		borderRadius: 10,
		padding: 12,
		gap: 6,
	},
	fallbackSource: {
		fontSize: 13,
		lineHeight: 18,
	},
	fallbackNote: {
		fontSize: 13,
	},
});
