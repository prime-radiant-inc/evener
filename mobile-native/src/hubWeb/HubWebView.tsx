import { useIsFocused } from "@react-navigation/native";
import { StatusBar } from "expo-status-bar";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Alert, AppState, Linking, View } from "react-native";
import { SafeAreaView, useSafeAreaInsets } from "react-native-safe-area-context";
import { WebView, type WebViewMessageEvent } from "react-native-webview";
import { useHoldAlerts, useWorkspaceDocument } from "../alerts/alertsContext";
import type { HubProfile } from "../connection";
import { useSystemGlass } from "../design/systemGlass";
import { paletteFor } from "../design/tokens";
import { haptic } from "../haptics";
import { workspaceSessionRef } from "../location";
import { externalMarkdownLink } from "../markdownLinks";
import type { WorkspaceShare } from "../share/sharePayload";
import { Button } from "../sheet/Grouped";
import { ErrorMessage, useColors } from "../ui";
import { type ButtonFrame, CreateSessionButton, isButtonFrame } from "./CreateSessionButton";
import { hubPagePath, hubPageSource } from "./hubWebPolicy";
import {
	type EdgeAction,
	EdgeSessionToolbar,
	type HeaderAction,
	isSessionHeader,
	SessionHeader,
	type SessionHeaderState,
	type SessionsToolbar,
} from "./SessionHeader";

interface HubWebViewProps {
	profile: HubProfile;
	token: string;
	initialPath: string;
	onPath(path: string): void;
	onHubMenu(): void;
	share?: WorkspaceShare;
	onSharePlaced?(id: string): void;
}

export function HubWebView({ profile, token, initialPath, onPath, onHubMenu, share, onSharePlaced }: HubWebViewProps) {
	const { origin } = profile;
	const { palette } = useColors();
	const web = useRef<WebView<unknown>>(null);
	const [entryPath, setEntryPath] = useState(initialPath);
	const currentPath = useRef(initialPath);
	const [bridgeReady, setBridgeReady] = useState(false);
	const [loaded, setLoaded] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [editing, setEditing] = useState(false);
	const [readingDocument, setReadingDocument] = useState(false);
	const [covered, setCovered] = useState(false);
	const [shareTarget, setShareTarget] = useState<{ path: string } | null>(null);
	const deliveredShare = useRef<WorkspaceShare | undefined>(undefined);
	const [header, setHeader] = useState<SessionHeaderState | null>(null);
	const [sessionsToolbar, setSessionsToolbar] = useState<SessionsToolbar | null>(null);
	const [restoringSession, setRestoringSession] = useState(() =>
		initialPath.startsWith("/s/") ? workspaceSessionRef(initialPath) : null,
	);
	const pendingHeaderAction = useRef<HeaderAction | null>(null);
	const endRestore = useCallback(() => {
		pendingHeaderAction.current = null;
		setRestoringSession(null);
	}, []);
	const [createButton, setCreateButton] = useState<{ id: string; frame: ButtonFrame } | null>(null);
	const [hubDark, setHubDark] = useState<boolean | null>(null);
	const focused = useIsFocused();
	const activeCreate = focused && !error ? createButton : null;
	useLayoutEffect(() => {
		if (activeCreate) web.current?.postMessage(JSON.stringify({ type: "createButtonShown", id: activeCreate.id }));
	}, [activeCreate]);
	const glass = useSystemGlass();
	const { top, left, right } = useSafeAreaInsets();
	// A vertical system bar reserves one window edge; landscape cutouts reserve both.
	const edgeToolbar = top === 0 && Math.min(left, right) === 0 && Math.max(left, right) >= 44;
	const material = JSON.stringify({ type: "material", glass, edgeToolbar });
	const headerAction = useCallback(
		(id: string | null, action: EdgeAction) => {
			if (id !== null) {
				const type = sessionsToolbar?.id === id ? "sessionsAction" : "headerAction";
				web.current?.postMessage(JSON.stringify({ type, id, action }));
			} else if (restoringSession && (action === "find" || action === "overview")) {
				pendingHeaderAction.current = action;
			}
		},
		[restoringSession, sessionsToolbar?.id],
	);
	useLayoutEffect(() => {
		if (!focused || error || readingDocument || covered) endRestore();
	}, [focused, error, readingDocument, covered, endRestore]);
	useLayoutEffect(() => {
		if (bridgeReady) web.current?.postMessage(material);
	}, [bridgeReady, material]);
	const inFront = bridgeReady && focused && !error;
	useEffect(() => {
		if (
			inFront &&
			!covered &&
			share?.hubId === profile.id &&
			share.path === shareTarget?.path &&
			initialPath === share.path
		) {
			deliveredShare.current = share;
			web.current?.postMessage(JSON.stringify({ type: "shareText", path: share.path, id: share.id, text: share.text }));
		}
	}, [inFront, covered, share, profile.id, shareTarget, initialPath]);
	useWorkspaceDocument(profile.id, readingDocument && bridgeReady && !error);
	useHoldAlerts(inFront && (editing || readingDocument), "quiet");
	useHoldAlerts(inFront && covered, "covered");
	const source = useMemo(() => {
		const page = hubPageSource(origin, entryPath, token);
		return page && { ...page, websiteDataStoreIdentifier: profile.id };
	}, [origin, entryPath, token, profile.id]);
	const shareFile = async (
		ref: string,
		path: string,
		namePath = path.startsWith("/") ? path.slice(path.lastIndexOf("/") + 1) : path,
	) => {
		try {
			const { shareSessionDocument } = await import("../reader/shareDocumentNative");
			await shareSessionDocument({ origin, token, sessionRef: ref, path, namePath });
		} catch {
			Alert.alert("Could not share this document", "It could not be saved from this hub. Try again.");
		}
	};
	const reportPath = (path: string) => {
		if (path === currentPath.current) return;
		endRestore();
		currentPath.current = path;
		setEditing(false);
		onPath(path);
	};
	const resetDocument = useCallback(() => {
		deliveredShare.current = undefined;
		setShareTarget(null);
		setBridgeReady(false);
		setEditing(false);
		setReadingDocument(false);
		setCovered(false);
		setHeader(null);
		setSessionsToolbar(null);
		setCreateButton(null);
		setHubDark(null);
	}, []);
	const retry = useCallback(() => {
		resetDocument();
		setEntryPath(currentPath.current);
		setLoaded(false);
		setError(null);
	}, [resetDocument]);
	useEffect(() => {
		if (!error) return;
		const subscription = AppState.addEventListener("change", (next) => {
			if (next === "active") retry();
		});
		const timer = setTimeout(() => {
			if (AppState.currentState === "active") retry();
		}, 8000);
		return () => {
			subscription.remove();
			clearTimeout(timer);
		};
	}, [error, retry]);
	// An alert can open a different session in the already-mounted workspace.
	// A location reported back by that same workspace needs no navigation.
	useEffect(() => {
		if (initialPath === currentPath.current) return;
		const path = hubPagePath(origin, origin + initialPath);
		if (!path) return;
		endRestore();
		currentPath.current = path;
		if (bridgeReady && web.current) web.current.postMessage(JSON.stringify({ type: "navigate", path }));
		else setEntryPath(path);
	}, [initialPath, bridgeReady, origin, endRestore]);

	function message({ nativeEvent }: WebViewMessageEvent) {
		const documentPath = hubPagePath(origin, nativeEvent.url);
		if (!documentPath) return;
		let data;
		try {
			data = JSON.parse(nativeEvent.data);
		} catch {
			return;
		}
		if (!data || typeof data !== "object") return;
		const onRestoredSession =
			restoringSession !== null &&
			workspaceSessionRef(documentPath) === restoringSession &&
			workspaceSessionRef(currentPath.current) === restoringSession &&
			workspaceSessionRef(initialPath) === restoringSession;
		switch (data.type) {
			case "shareTarget":
				if (data.path === documentPath) setShareTarget({ path: documentPath });
				break;
			case "sharePlaced":
				if (
					focused &&
					!error &&
					share &&
					deliveredShare.current === share &&
					share.hubId === profile.id &&
					share.id === data.id &&
					data.path === share.path &&
					documentPath === share.path &&
					initialPath === share.path
				) {
					deliveredShare.current = undefined;
					onSharePlaced?.(share.id);
				}
				break;
			case "appearance":
				if (typeof data.dark === "boolean") setHubDark(data.dark);
				break;
			case "sessionHeader": {
				const next = isSessionHeader(data.header) ? data.header : null;
				const pending = pendingHeaderAction.current;
				if (
					next &&
					onRestoredSession &&
					(next.sessionRef === undefined || next.sessionRef === restoringSession) &&
					pending &&
					focused &&
					!error
				) {
					headerAction(next.id, pending);
				}
				endRestore();
				setHeader(next);
				break;
			}
			case "createButton":
				if (typeof data.id !== "string" || !data.id) break;
				if (isButtonFrame(data.frame)) setCreateButton({ id: data.id, frame: data.frame });
				else if (data.frame === null) setCreateButton((current) => (current?.id === data.id ? null : current));
				break;
			case "sessionsToolbar":
				if (typeof data.id !== "string" || !data.id) break;
				if (data.mode === "list" || data.mode === "launcher") {
					endRestore();
					setSessionsToolbar({ id: data.id, mode: data.mode });
				} else if (
					data.mode === "navigation" &&
					typeof data.back === "boolean" &&
					typeof data.needsYou === "number" &&
					Number.isSafeInteger(data.needsYou) &&
					data.needsYou >= 0 &&
					(data.destination === null || data.destination === "open" || data.destination === "board")
				) {
					endRestore();
					setSessionsToolbar({
						id: data.id,
						mode: data.mode,
						back: data.back,
						destination: data.destination,
						needsYou: data.needsYou,
					});
				} else if (data.mode === null) {
					setSessionsToolbar((current) => (current?.id === data.id ? null : current));
				}
				break;
			case "ready":
				if (!onRestoredSession) endRestore();
				if (bridgeReady) web.current?.postMessage(material);
				else setBridgeReady(true);
				break;
			case "route": {
				const path = typeof data.path === "string" && hubPagePath(origin, origin + data.path);
				if (path) reportPath(path);
				break;
			}
			case "hubMenu":
				setEditing(false);
				onHubMenu();
				break;
			case "editing":
				if (typeof data.active === "boolean") setEditing(data.active);
				break;
			case "readingDocument":
				if (typeof data.active === "boolean") setReadingDocument(data.active);
				break;
			case "covered":
				if (typeof data.active === "boolean") setCovered(data.active);
				break;
			case "shareDocument":
				if (typeof data.ref === "string" && data.ref && typeof data.path === "string" && data.path)
					void shareFile(data.ref, data.path, typeof data.namePath === "string" ? data.namePath : undefined);
				break;
			case "haptic":
				if (data.kind === "light") haptic(data.kind);
				break;
		}
	}
	function openLink(address: string) {
		const href = externalMarkdownLink(address);
		if (!href) return;
		const url = new URL(href);
		if (url.origin === origin) {
			const ref = url.searchParams.get("session");
			const path = url.searchParams.get("path");
			if (url.pathname === "/doc/file" && url.searchParams.get("format") === "download" && ref && path) {
				void shareFile(ref, path);
			} else {
				const page = hubPagePath(origin, href);
				if (page && page !== currentPath.current)
					web.current?.injectJavaScript(`location.assign(${JSON.stringify(page)}); true;`);
			}
		} else {
			void Linking.openURL(href).catch(() => Alert.alert("Could not open this link"));
		}
	}
	if (error || !source)
		return (
			<SafeAreaView
				style={{
					flex: 1,
					justifyContent: "center",
					alignItems: "center",
					gap: 12,
					paddingHorizontal: 16,
					backgroundColor: palette.canvas,
				}}
			>
				<ErrorMessage message={error ?? "Edit the saved token to open this hub on iPhone."} />
				{source && <Button label="Try again" primary compact onPress={retry} />}
				<Button label="Hubs on this iPhone" onPress={onHubMenu} />
			</SafeAreaView>
		);
	const activeHeader = focused && (edgeToolbar || !header?.split) ? header : null;
	const hubPalette = focused && hubDark !== null ? paletteFor(hubDark ? "dark" : "light") : null;
	return (
		<SafeAreaView
			edges={["top", "left", "right"]}
			style={{ flex: 1, backgroundColor: hubPalette?.surface ?? palette.canvas }}
		>
			{hubPalette && <StatusBar style={hubPalette.scheme === "dark" ? "light" : "dark"} />}
			{loaded && !activeHeader && (!bridgeReady || currentPath.current.startsWith("/thread/")) && (
				<View style={{ alignItems: "flex-end" }}>
					<Button label="Hubs on this iPhone" onPress={onHubMenu} />
				</View>
			)}
			<View style={{ flex: 1 }}>
				<WebView<unknown>
					ref={web}
					source={source}
					applicationNameForUserAgent="EvenerNative/1 EvenerSessionHeader/1 EvenerWorkspaceControls/1 EvenerSessionsToolbar/1 EvenerPaneNavigation/1"
					injectedJavaScriptBeforeContentLoaded={`window.evenerEdgeToolbar = ${edgeToolbar}; true;`}
					// A whitelist miss makes RNCWebView open the URL in the OS itself.
					// Route every scheme through our user-navigation check instead.
					originWhitelist={["*"]}
					onShouldStartLoadWithRequest={(request) => {
						if (hubPagePath(origin, request.url)) return true;
						if (request.isTopFrame) {
							if (request.navigationType === "click") openLink(request.url);
							else setError("The hub redirected outside its workspace.");
						}
						return false;
					}}
					onOpenWindow={({ nativeEvent }) => openLink(nativeEvent.targetUrl)}
					onMessage={message}
					onLoadStart={resetDocument}
					onLoad={({ nativeEvent }) => {
						setLoaded(true);
						const path = hubPagePath(origin, nativeEvent.url);
						if (path) reportPath(path);
					}}
					onError={() => setError(`Could not reach ${profile.name}.`)}
					onHttpError={({ nativeEvent }) => {
						if (hubPagePath(origin, nativeEvent.url)) {
							setError(
								nativeEvent.statusCode === 401
									? "Check the token in this hub’s saved connection."
									: `The hub returned HTTP ${nativeEvent.statusCode}.`,
							);
						}
					}}
					onContentProcessDidTerminate={() => setError("Reopening the hub workspace…")}
					startInLoadingState
					renderLoading={() => (
						<View
							style={{
								position: "absolute",
								inset: 0,
								justifyContent: "center",
								alignItems: "center",
								gap: 12,
								backgroundColor: palette.canvas,
							}}
						>
							<ActivityIndicator accessibilityLabel={`Opening ${profile.name}`} />
							<Button label="Hubs on this iPhone" onPress={onHubMenu} />
						</View>
					)}
					automaticallyAdjustContentInsets={false}
					contentInsetAdjustmentBehavior="never"
					contentMode="mobile"
					allowsBackForwardNavigationGestures
					hideKeyboardAccessoryView
					style={{ backgroundColor: (hubPalette ?? palette).canvas }}
				/>
				{edgeToolbar && focused && (sessionsToolbar || activeHeader || restoringSession) ? (
					<EdgeSessionToolbar
						header={activeHeader}
						sessionsToolbar={sessionsToolbar}
						palette={hubPalette ?? palette}
						onAction={headerAction}
					/>
				) : activeHeader ? (
					<SessionHeader header={activeHeader} glass={glass} palette={hubPalette ?? palette} onAction={headerAction} />
				) : null}
				{activeCreate && (
					<CreateSessionButton
						frame={activeCreate.frame}
						palette={hubPalette ?? palette}
						glass={glass}
						onPress={() => web.current?.postMessage(JSON.stringify({ type: "createSession", id: activeCreate.id }))}
					/>
				)}
			</View>
		</SafeAreaView>
	);
}
