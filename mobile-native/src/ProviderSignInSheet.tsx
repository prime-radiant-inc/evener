// Signing a provider in without leaving the app (spec 12): the hub's device
// flow hands the phone a page and a code, so the sheet copies the code on the
// way, opens the page in the in-app browser, and closes it again once the
// flow's own polling sees the sign-in land.
import * as Clipboard from "expo-clipboard";
import { SymbolView } from "expo-symbols";
import * as WebBrowser from "expo-web-browser";
import { type ReactNode, useEffect, useState, useSyncExternalStore } from "react";
import { AppState, Text, TextInput, View } from "react-native";
import { fonts, scaledType, space, uiType } from "./design/tokens";
import type { ProviderSignIn } from "./providerSignIn";
import { Button, Group, GroupedPage, GroupFooter, Row } from "./sheet/Grouped";
import { ModalSheet } from "./sheet/ModalSheet";
import { SheetStatus } from "./sheet/SheetStatus";
import { Spinner } from "./sheet/Spinner";
import { allowFontScaling, useColors, useTextScale } from "./ui";

/** What went wrong on the phone's side of the sheet. */
type LocalProblem = "copyFailed" | "openFailed";

const LOCAL_PROBLEMS: Record<LocalProblem, string> = {
	copyFailed: "Could not copy the code. Select it to copy manually.",
	openFailed: "Could not open the sign-in page.",
};

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
	const [localProblem, setLocalProblem] = useState<LocalProblem | null>(null);
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
		if (!authorized) return;
		WebBrowser.dismissBrowser().catch(() => {});
		// A copy or open that failed earlier doesn't matter once signed in.
		setLocalProblem(null);
	}, [authorized]);
	async function copy(flowId: string, code: string) {
		let copied = false;
		try {
			copied = await Clipboard.setStringAsync(code);
		} catch {}
		// A failed copy takes back an earlier "Code copied".
		setCopiedFlow(copied ? flowId : null);
		if (!copied) setLocalProblem("copyFailed");
	}
	// openBrowserAsync resolves only when the page closes, so the waiting state
	// starts as it opens and is taken back if it couldn't.
	// Only a plain http(s) page with no user info opens.
	function openable(url: string): boolean {
		try {
			const parsed = new URL(url);
			return ["https:", "http:"].includes(parsed.protocol) && !parsed.username && !parsed.password;
		} catch {
			return false;
		}
	}
	async function open(url: string, onOpening?: (opening: boolean) => void) {
		try {
			if (!openable(url)) throw new Error("Unsupported URL");
			onOpening?.(true);
			await WebBrowser.openBrowserAsync(url);
		} catch {
			onOpening?.(false);
			setLocalProblem("openFailed");
		}
	}
	const device = state.phase === "device" ? state.device : undefined;
	const waiting = !!device && openedFlow === device.flowId;
	const openDevicePage = async () => {
		if (!device) return;
		setLocalProblem(null);
		// Nothing is copied for a page that won't open.
		if (!openable(device.verificationUrl)) {
			setLocalProblem("openFailed");
			return;
		}
		await copy(device.flowId, device.userCode);
		await open(device.verificationUrl, (opening) => setOpenedFlow(opening ? device.flowId : null));
	};
	const startAgain = () => {
		setLocalProblem(null);
		setRedirect("");
		void flow.start();
	};
	const body = (text: ReactNode) => (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 21 * scale }}
		>
			{text}
		</Text>
	);
	const statement = (text: string, testID?: string) => (
		<Text
			allowFontScaling={allowFontScaling}
			testID={testID}
			style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600" }}
		>
			{text}
		</Text>
	);
	const flowProblem = state.problem ? `sign-in-problem:${state.problem}` : undefined;
	const copyState = (flowId: string) => `copy-code:${copiedFlow === flowId ? "copied" : "uncopied"}`;
	return (
		<ModalSheet
			title={`Sign in to ${name}`}
			onCancel={authorized ? undefined : onClose}
			done={authorized ? { onPress: onClose } : undefined}
			accessory={<SheetStatus />}
			onRequestClose={onClose}
		>
			<GroupedPage>
				{state.phase === "idle" || state.phase === "starting" ? <Spinner label="Starting sign-in" /> : null}
				{device && !waiting ? (
					// As the prototype draws it (hub.js 128-132): the code with Copy
					// code beside it, the page's one call to action, and how long the
					// code lasts. A poll's error or a failed copy or open lands in a
					// footer right under it.
					<Section kind="device" followedBy={state.error || localProblem ? "footer" : undefined}>
						{body(
							"The sign-in page opens inside the app, and this code is copied for you. Paste it when the page asks for it. The hub finishes signing in on its own.",
						)}
						<View
							testID={copyState(device.flowId)}
							style={{
								flexDirection: "row",
								// At the largest text sizes Copy code moves under the code.
								flexWrap: "wrap",
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
							<Button
								label={copiedFlow === device.flowId ? "Code copied" : "Copy code"}
								onPress={() => {
									setLocalProblem(null);
									void copy(device.flowId, device.userCode);
								}}
							/>
						</View>
						<Button
							label="Open sign-in page"
							primary
							onPress={() => {
								void openDevicePage();
							}}
						/>
						{/* The hub drops a device flow after hubAuthFlowTTL, 15 minutes
						    (app_auth.go); the provider's code expires in about that time. */}
						<Text
							allowFontScaling={allowFontScaling}
							testID="sign-in-code-expiry"
							style={{ color: palette.inkLow, ...scaledType(uiType.footnote, scale) }}
						>
							The code expires in 15 minutes.
						</Text>
					</Section>
				) : null}
				{device && waiting ? (
					<>
						<Section kind="code" centered followedBy="group">
							{/* After a failed poll nothing is being waited on: the next
							    check is the person's (Check again, below). */}
							{state.error ? null : statement("Waiting for you to finish signing in…", "sign-in-waiting")}
							{/* The code stays selectable, and copyable, in case the
							    automatic copy failed. */}
							{body(
								<>
									Your code is{" "}
									<Text selectable style={{ fontFamily: fonts.mono }}>
										{device.userCode}
									</Text>
									.
								</>,
							)}
						</Section>
						<Group>
							<Row
								label="Open sign-in page"
								tone="accent"
								onPress={() => {
									void openDevicePage();
								}}
							/>
							<Row
								label={copiedFlow === device.flowId ? "Code copied" : "Copy code"}
								testID={copyState(device.flowId)}
								tone="accent"
								onPress={() => {
									setLocalProblem(null);
									void copy(device.flowId, device.userCode);
								}}
							/>
						</Group>
					</>
				) : null}
				{device && state.error ? (
					<>
						<GroupFooter tone="danger" testID={flowProblem}>
							{state.error}
						</GroupFooter>
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
						<Section kind="redirect" followedBy="group">
							{body("Open the sign-in page and sign in, then paste the full redirect URL here.")}
						</Section>
						<Group>
							<Row
								label="Open sign-in page"
								tone="accent"
								onPress={() => {
									setLocalProblem(null);
									if (state.browser) void open(state.browser.url);
								}}
							/>
						</Group>
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
						{state.error ? (
							<GroupFooter tone="danger" testID={flowProblem}>
								{state.error}
							</GroupFooter>
						) : null}
					</>
				) : null}
				{state.phase === "expired" || state.phase === "error" ? (
					<>
						{/* An expired code's flow error sends you to check the status by
						 * hand, a step the sheet no longer has, so it says what happened. */}
						{state.phase === "expired" ? (
							<Section kind="expired" followedBy="group">
								{statement("The code expired.")}
							</Section>
						) : state.error ? (
							<Section kind="failed" followedBy="group">
								{statement(state.error, flowProblem)}
							</Section>
						) : null}
						<Group>
							<Row label="Start again" tone="accent" disabled={!connected || state.busy} onPress={startAgain} />
						</Group>
					</>
				) : null}
				{authorized ? (
					<Section kind="signed-in" centered>
						<SymbolView name="checkmark" tintColor={palette.aliveInk} size={34} />
						{statement(`Signed in to ${name}`)}
						{body("Sessions using it can continue.")}
					</Section>
				) : null}
				{localProblem ? (
					<GroupFooter tone="danger" testID={`sign-in-problem:${localProblem}`}>
						{LOCAL_PROBLEMS[localProblem]}
					</GroupFooter>
				) : null}
			</GroupedPage>
		</ModalSheet>
	);
}

/** A stretch of the sheet outside any group: its prose, the code, a button.
 * What follows it may bring its own space: a group its 16pt, so over one the
 * bottom padding is only what's left; a footer its own top padding, so over
 * one the stretch adds none. Its `kind` names which stretch it is, as its
 * `sign-in-stretch:<kind>` testID. */
function Section({
	kind,
	children,
	centered = false,
	followedBy,
}: {
	kind: "device" | "code" | "redirect" | "expired" | "failed" | "signed-in";
	children: ReactNode;
	centered?: boolean;
	followedBy?: "group" | "footer";
}) {
	const edge = centered ? 28 : 16;
	return (
		<View
			testID={`sign-in-stretch:${kind}`}
			style={{
				paddingHorizontal: 20,
				paddingTop: edge,
				paddingBottom: followedBy === "group" ? edge - space.groupGap : followedBy === "footer" ? 0 : edge,
				gap: centered ? 8 : 16,
				alignItems: centered ? "center" : "stretch",
			}}
		>
			{children}
		</View>
	);
}
