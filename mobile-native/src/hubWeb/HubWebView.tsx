import { useIsFocused } from "@react-navigation/native";
import { StatusBar } from "expo-status-bar";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Alert, AppState, Linking, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { WebView, type WebViewMessageEvent } from "react-native-webview";
import { useHoldAlerts } from "../alerts/alertsContext";
import type { HubProfile } from "../connection";
import { useSystemGlass } from "../design/systemGlass";
import { paletteFor } from "../design/tokens";
import { haptic } from "../haptics";
import { externalMarkdownLink } from "../markdownLinks";
import { HeaderButton } from "../sheet/HeaderButton";
import { ErrorMessage, useColors } from "../ui";
import { hubPagePath, hubPageSource } from "./hubWebPolicy";
import { isSessionHeader, SessionHeader, type SessionHeaderState } from "./SessionHeader";

interface HubWebViewProps {
	profile: HubProfile;
	token: string;
	initialPath: string;
	onPath(path: string): void;
	onHubMenu(): void;
	onDeviceWork(): void;
}

export function HubWebView({ profile, token, initialPath, onPath, onHubMenu, onDeviceWork }: HubWebViewProps) {
	const { origin } = profile;
	const { palette } = useColors();
	const web = useRef<WebView<unknown>>(null);
	const [entryPath, setEntryPath] = useState(initialPath);
	const currentPath = useRef(initialPath);
	const [bridgeReady, setBridgeReady] = useState(false);
	const [loaded, setLoaded] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [editing, setEditing] = useState(false);
	const [header, setHeader] = useState<SessionHeaderState | null>(null);
	const [hubDark, setHubDark] = useState<boolean | null>(null);
	const focused = useIsFocused();
	const glass = useSystemGlass();
	const material = JSON.stringify({ type: "material", glass });
	useEffect(() => {
		if (bridgeReady) web.current?.postMessage(material);
	}, [bridgeReady, material]);
	useHoldAlerts(editing && bridgeReady && focused && !error, "quiet");
	const source = useMemo(() => {
		const page = hubPageSource(origin, entryPath, token);
		return page && { ...page, websiteDataStoreIdentifier: profile.id };
	}, [origin, entryPath, token, profile.id]);
	const shareFile = async (ref: string, path: string) => {
		try {
			const { shareSessionDocument } = await import("../reader/shareDocumentNative");
			await shareSessionDocument({ origin, token, sessionRef: ref, path });
		} catch {
			Alert.alert("Could not share this document", "It could not be saved from this hub. Try again.");
		}
	};
	const reportPath = (path: string) => {
		if (path === currentPath.current) return;
		currentPath.current = path;
		setEditing(false);
		onPath(path);
	};
	const retry = useCallback(() => {
		setEntryPath(currentPath.current);
		setBridgeReady(false);
		setEditing(false);
		setLoaded(false);
		setHeader(null);
		setHubDark(null);
		setError(null);
	}, []);
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
		currentPath.current = path;
		if (bridgeReady && web.current) web.current.postMessage(JSON.stringify({ type: "navigate", path }));
		else setEntryPath(path);
	}, [initialPath, bridgeReady, origin]);

	function message({ nativeEvent }: WebViewMessageEvent) {
		if (!hubPagePath(origin, nativeEvent.url)) return;
		let data;
		try {
			data = JSON.parse(nativeEvent.data);
		} catch {
			return;
		}
		if (!data || typeof data !== "object") return;
		switch (data.type) {
			case "appearance":
				if (typeof data.dark === "boolean") setHubDark(data.dark);
				break;
			case "sessionHeader":
				setHeader(isSessionHeader(data.header) ? data.header : null);
				break;
			case "ready":
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
			case "shareDocument":
				if (typeof data.ref === "string" && data.ref && typeof data.path === "string" && data.path)
					void shareFile(data.ref, data.path);
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
			<SafeAreaView style={{ flex: 1, justifyContent: "center", backgroundColor: palette.canvas }}>
				<ErrorMessage message={error ?? "Edit the saved token to open this hub on iPhone."} />
				{source && <HeaderButton label="Try again" strong onPress={retry} />}
				<HeaderButton label="iPhone settings" onPress={onHubMenu} />
				<HeaderButton label="Work on this iPhone" onPress={onDeviceWork} />
			</SafeAreaView>
		);
	const activeHeader = focused ? header : null;
	const hubPalette = focused && hubDark !== null ? paletteFor(hubDark ? "dark" : "light") : null;
	return (
		<SafeAreaView
			edges={["top", "left", "right"]}
			style={{ flex: 1, backgroundColor: hubPalette?.surface ?? palette.canvas }}
		>
			{hubPalette && <StatusBar style={hubPalette.scheme === "dark" ? "light" : "dark"} />}
			{loaded && !activeHeader && (!bridgeReady || currentPath.current.startsWith("/thread/")) && (
				<View style={{ alignItems: "flex-end" }}>
					<HeaderButton label="iPhone settings" onPress={onHubMenu} />
				</View>
			)}
			<View style={{ flex: 1 }}>
				<WebView<unknown>
					ref={web}
					source={source}
					applicationNameForUserAgent="EvenerNative/1 EvenerSessionHeader/1"
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
					onLoadStart={() => {
						setHeader(null);
						setHubDark(null);
						setEditing(false);
						setBridgeReady(false);
					}}
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
									? "Check the token in iPhone settings."
									: `The hub returned HTTP ${nativeEvent.statusCode}.`,
							);
						}
					}}
					onContentProcessDidTerminate={() => setError("Reopening the hub workspace…")}
					startInLoadingState
					renderLoading={() => (
						<View style={{ position: "absolute", inset: 0, justifyContent: "center", backgroundColor: palette.canvas }}>
							<ActivityIndicator accessibilityLabel={`Opening ${profile.name}`} />
							<HeaderButton label="iPhone settings" onPress={onHubMenu} />
						</View>
					)}
					automaticallyAdjustContentInsets={false}
					contentInsetAdjustmentBehavior="never"
					contentMode="mobile"
					allowsBackForwardNavigationGestures
					hideKeyboardAccessoryView
					style={{ backgroundColor: (hubPalette ?? palette).canvas }}
				/>
				{activeHeader && (
					<SessionHeader
						header={activeHeader}
						glass={glass}
						palette={hubPalette ?? palette}
						onAction={(action) =>
							web.current?.postMessage(JSON.stringify({ type: "headerAction", id: activeHeader.id, action }))
						}
					/>
				)}
			</View>
		</SafeAreaView>
	);
}
