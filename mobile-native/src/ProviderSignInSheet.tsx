// Signing a provider in without leaving the app (spec 12): the hub's device
// flow hands the phone a page and a code, so the sheet copies the code on the
// way, opens the page in the in-app browser, and closes it again once the
// flow's own polling sees the sign-in land.
import * as Clipboard from "expo-clipboard";
import { SymbolView } from "expo-symbols";
import * as WebBrowser from "expo-web-browser";
import { type ReactNode, useEffect, useState, useSyncExternalStore } from "react";
import { ActivityIndicator, AppState, Modal, Pressable, Text, TextInput, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { fonts } from "./design/tokens";
import type { ProviderSignIn } from "./providerSignIn";
import { Group, GroupedPage, GroupFooter, Row } from "./sheet/Grouped";
import { SheetStatus } from "./sheet/SheetStatus";
import { Action, allowFontScaling, useColors, useTextScale } from "./ui";

const COPY_FAILED = "Could not copy the code. Select it to copy manually.";
const OPEN_FAILED = "Could not open the sign-in page.";

export function ProviderSignInSheet({
	flow,
	name,
	connected,
	onClose,
}: {
	flow: ProviderSignIn;
	name: string;
	connected: boolean;
	onClose(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const state = useSyncExternalStore(flow.subscribe, flow.getSnapshot);
	const [redirect, setRedirect] = useState("");
	const [localError, setLocalError] = useState<string | null>(null);
	// Both name the device flow they happened in, so a flow started again
	// begins uncopied and unopened without resetting them.
	const [copiedFlow, setCopiedFlow] = useState<string | null>(null);
	const [openedFlow, setOpenedFlow] = useState<string | null>(null);
	useEffect(() => {
		flow.setActive(AppState.currentState === "active");
		const subscription = AppState.addEventListener("change", (value) => flow.setActive(value === "active"));
		return () => subscription.remove();
	}, [flow]);
	const authorized = state.phase === "authorized";
	useEffect(() => {
		// The sign-in landed while the page may still be open over the app.
		// With no browser open the call rejects, which is nothing to report.
		if (authorized) WebBrowser.dismissBrowser().catch(() => {});
	}, [authorized]);
	async function copy(flowId: string, code: string) {
		try {
			if (await Clipboard.setStringAsync(code)) setCopiedFlow(flowId);
			else setLocalError(COPY_FAILED);
		} catch {
			setLocalError(COPY_FAILED);
		}
	}
	async function open(url: string, onOpening?: () => void) {
		try {
			const parsed = new URL(url);
			if (!["https:", "http:"].includes(parsed.protocol) || parsed.username || parsed.password)
				throw new Error("Unsupported URL");
			onOpening?.();
			await WebBrowser.openBrowserAsync(url);
		} catch {
			setLocalError(OPEN_FAILED);
		}
	}
	const device = state.phase === "device" ? state.device : undefined;
	const waiting = !!device && openedFlow === device.flowId;
	const openDevicePage = async () => {
		if (!device) return;
		setLocalError(null);
		await copy(device.flowId, device.userCode);
		await open(device.verificationUrl, () => setOpenedFlow(device.flowId));
	};
	const startAgain = () => {
		setLocalError(null);
		setRedirect("");
		void flow.start();
	};
	const body = (text: string) => (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 21 * scale }}
		>
			{text}
		</Text>
	);
	const statement = (text: string) => (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600" }}
		>
			{text}
		</Text>
	);
	return (
		<Modal visible animationType="slide" presentationStyle="pageSheet" onRequestClose={onClose}>
			<SafeAreaView style={{ flex: 1, backgroundColor: palette.canvas }}>
				<Header title={`Sign in to ${name}`} close={authorized ? "Done" : "Cancel"} onClose={onClose} />
				<SheetStatus />
				<GroupedPage>
					{state.phase === "idle" || state.phase === "starting" ? (
						<ActivityIndicator accessibilityLabel="Starting sign-in" style={{ padding: 32 }} />
					) : null}
					{device && !waiting ? (
						<Section>
							{body(
								"The sign-in page opens inside the app, and this code is copied for you. Paste it when the page asks for it. The hub finishes signing in on its own.",
							)}
							<View
								style={{
									flexDirection: "row",
									alignItems: "center",
									justifyContent: "space-between",
									gap: 12,
									borderRadius: 12,
									paddingHorizontal: 16,
									paddingVertical: 14,
									backgroundColor: palette.surface,
								}}
							>
								<Text
									selectable
									allowFontScaling={allowFontScaling}
									style={{
										color: palette.inkHi,
										fontFamily: fonts.mono,
										fontWeight: "600",
										fontSize: 26 * scale,
										letterSpacing: 2,
										flexShrink: 1,
									}}
								>
									{device.userCode}
								</Text>
								<Action
									label="Copy code"
									onPress={() => {
										setLocalError(null);
										void copy(device.flowId, device.userCode);
									}}
								>
									{copiedFlow === device.flowId ? "Code copied" : "Copy code"}
								</Action>
							</View>
							<Action
								tone="primary"
								onPress={() => {
									void openDevicePage();
								}}
							>
								Open sign-in page
							</Action>
						</Section>
					) : null}
					{device && waiting ? (
						<>
							<Section centered>
								{statement("Waiting for you to finish signing in…")}
								{body(`Your code is ${device.userCode}.`)}
							</Section>
							<Group>
								<Row
									label="Open sign-in page"
									tone="accent"
									onPress={() => {
										void openDevicePage();
									}}
								/>
							</Group>
						</>
					) : null}
					{device && state.error ? (
						<>
							<GroupFooter tone="danger">{state.error}</GroupFooter>
							<Group>
								<Row
									label="Check again"
									tone="accent"
									disabled={!connected || state.busy}
									onPress={() => {
										void flow.retryPoll();
									}}
								/>
							</Group>
						</>
					) : null}
					{state.phase === "browser" && state.browser ? (
						<>
							<Section>{body("Open the sign-in page and sign in, then paste the full redirect URL here.")}</Section>
							<Group>
								<Row
									label="Open sign-in page"
									tone="accent"
									onPress={() => {
										setLocalError(null);
										if (state.browser) void open(state.browser.url);
									}}
								/>
							</Group>
							<View style={{ height: 20 }} />
							<Group>
								<TextInput
									accessibilityLabel="Redirect URL"
									placeholder="Paste the redirect URL"
									placeholderTextColor={palette.inkLow}
									allowFontScaling={allowFontScaling}
									value={redirect}
									onChangeText={setRedirect}
									autoCapitalize="none"
									autoCorrect={false}
									editable={!state.busy}
									style={{
										color: palette.inkHi,
										fontSize: 17 * scale,
										minHeight: 44,
										paddingHorizontal: 16,
										paddingVertical: 11,
									}}
								/>
								<Row
									label="Finish sign-in"
									tone="accent"
									disabled={!connected || state.busy || !redirect.trim()}
									onPress={() => {
										const value = redirect;
										setRedirect("");
										void flow.complete(value);
									}}
								/>
							</Group>
							{state.error ? <GroupFooter tone="danger">{state.error}</GroupFooter> : null}
						</>
					) : null}
					{state.phase === "expired" || state.phase === "error" ? (
						<>
							{/* An expired code's flow error sends you to check the status by
							 * hand, a step the sheet no longer has, so it says what happened. */}
							{state.phase === "expired" ? (
								<Section>{statement("The code expired.")}</Section>
							) : state.error ? (
								<Section>{statement(state.error)}</Section>
							) : null}
							<Group>
								<Row label="Start again" tone="accent" disabled={!connected || state.busy} onPress={startAgain} />
							</Group>
						</>
					) : null}
					{authorized ? (
						<Section centered>
							<SymbolView name="checkmark" tintColor={palette.aliveInk} size={34} />
							{statement(`Signed in to ${name}`)}
							{body("Sessions using it can continue.")}
						</Section>
					) : null}
					{localError ? <GroupFooter tone="danger">{localError}</GroupFooter> : null}
				</GroupedPage>
			</SafeAreaView>
		</Modal>
	);
}

/** A stretch of the sheet outside any group: its prose, the code, a button. */
function Section({ children, centered = false }: { children: ReactNode; centered?: boolean }) {
	return (
		<View
			style={{
				paddingHorizontal: 20,
				paddingVertical: centered ? 28 : 16,
				gap: centered ? 8 : 16,
				alignItems: centered ? "center" : "stretch",
			}}
		>
			{children}
		</View>
	);
}

/** The sheet's header: Cancel, or Done once signed in, and the title. */
function Header({ title, close, onClose }: { title: string; close: "Cancel" | "Done"; onClose(): void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ flexDirection: "row", alignItems: "center", minHeight: 44, paddingHorizontal: 16 }}>
			<View style={{ flex: 1, alignItems: "flex-start" }}>
				<Pressable accessibilityRole="button" accessibilityLabel={close} hitSlop={8} onPress={onClose}>
					<Text
						allowFontScaling={allowFontScaling}
						style={{
							color: palette.accentInk,
							fontSize: 17 * scale,
							fontWeight: close === "Done" ? "600" : "400",
						}}
					>
						{close}
					</Text>
				</Pressable>
			</View>
			<Text
				accessibilityRole="header"
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				style={{ flex: 2, textAlign: "center", color: palette.inkHi, fontSize: 17 * scale, fontWeight: "600" }}
			>
				{title}
			</Text>
			<View style={{ flex: 1 }} />
		</View>
	);
}
