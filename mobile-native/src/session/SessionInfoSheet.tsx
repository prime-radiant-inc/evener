// The Session sheet (spec 8.6): everything the session knows about itself,
// and what you can do to it. A formSheet route that opens at large
// (sheetRoutes.ts). The session screen provides its live session, controls
// and actions through sessionInfoHosts (ruling 37); the sheet shows only
// facts the thread carries (ruling 21, with S15's access).
import { useNavigation } from "@react-navigation/native";
import type { NativeStackNavigationProp, NativeStackScreenProps } from "@react-navigation/native-stack";
import { type SFSymbol, SymbolView } from "expo-symbols";
import { Children, type ReactElement, type ReactNode, useState } from "react";
import { Alert, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { markFor } from "../board/StateMark";
import { typeRoles } from "../design/tokens";
import type { MobileConversation } from "../projectedRows";
import type { Routes } from "../screens";
import { type SessionControls, useControlsState } from "../sessionControls";
import { Sheet, useSheet } from "../sheet/Sheet";
import { sheetHosts, sheetKey, useSheetHost } from "../sheet/sheetHosts";
import { Toast, type ToastController, type ToastMessage, useToast } from "../Toast";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { wrapAfterSlashes } from "./format";
import {
	accessFacts,
	canDeleteSavedSession,
	latestForkPoint,
	notesSummary,
	pluginsLine,
	usageFacts,
	visionModelLabel,
	whereFacts,
} from "./sessionFacts";
import { SHUT_DOWN, sessionStateLine } from "./sessionState";

export type SessionInfoAction = "aside" | "fork" | "compact" | "pin" | "archive" | "shutDown" | "delete";

export interface SessionInfoHost {
	session: MobileConversation;
	/** Null while the hub is away: the sheet still shows what it knows. */
	controls: SessionControls | null;
	hostLabel(hostId: string): string;
	/** The model and its effort, as the composer's chip names them. */
	modelLabel: string;
	/** Whether the session can take an action now: connected, open and idle. */
	ready: boolean;
	editGoal(): void;
	clearGoal(): void;
	/** Runs the action as the ⋯ menu does, and returns its toast. */
	act(action: SessionInfoAction): Promise<ToastMessage | null>;
	/** The session's own toast, for an action that closed the sheet. */
	toast(message: ToastMessage): void;
}

export const sessionInfoHosts = sheetHosts<SessionInfoHost>();

/** Asks before shutting a session down. The ⋯ menu and this sheet ask the
 * same question. */
export function confirmShutDown(shutDown: () => void): void {
	Alert.alert("Shut down this session?", "It stops now and keeps its history. Sending a message resumes it.", [
		{ text: "Cancel", style: "cancel" },
		{ text: "Shut down", style: "destructive", onPress: shutDown },
	]);
}

// The model sheet's actions (Task 21) show their own errors there.
const MODEL_ACTIONS = new Set(["changeModel", "setReasoningEffort", "setVisionModel"]);

const GOAL_STATUS: Record<string, string> = { active: "Active", complete: "Complete", blocked: "Blocked" };

export function SessionInfoSheet({ route }: NativeStackScreenProps<Routes, "SessionInfoSheet">) {
	const { hubId, ref } = route.params;
	const toast = useToast();
	// The name being typed, and the name it started from. Null while the
	// title isn't being edited.
	const [editing, setEditing] = useState<{ text: string; from: string } | null>(null);
	const sheet = useSheet({
		dirty: editing !== null && editing.text !== editing.from,
		discardTitle: "Discard the new name?",
	});
	const host = useSheetHost(sessionInfoHosts, sheetKey(hubId, ref), sheet);
	if (!host) return null;
	return (
		<Sheet done={{ onPress: () => sheet.finish() }} accessory={<Toast toast={toast.toast} dismiss={toast.dismiss} />}>
			<SessionInfoBody
				host={host}
				hubId={hubId}
				sessionRef={ref}
				editing={editing}
				setEditing={setEditing}
				finish={sheet.finish}
				toast={toast}
			/>
		</Sheet>
	);
}

function SessionInfoBody({
	host,
	hubId,
	sessionRef,
	editing,
	setEditing,
	finish,
	toast,
}: {
	host: SessionInfoHost;
	hubId: string;
	sessionRef: string;
	editing: { text: string; from: string } | null;
	setEditing(editing: { text: string; from: string } | null): void;
	finish(then?: () => void): void;
	toast: ToastController;
}) {
	const { palette } = useColors();
	const navigation = useNavigation<NativeStackNavigationProp<Routes>>();
	const state = useControlsState(host.controls);
	const { session, ready } = host;
	const capabilities = session.capabilities;
	const where = whereFacts(session, host.hostLabel);
	const plugins = pluginsLine(session);
	const access = accessFacts(session);
	const usage = usageFacts(session);
	const forkPoint = latestForkPoint(session.items);
	const shutDown = SHUT_DOWN.has(session.status.type);
	// An action that leads away closes the sheet first, then hands the action
	// to the session, which shows its toast (ruling 37).
	const leaveFor = (action: SessionInfoAction) =>
		finish(() => {
			navigation.goBack();
			void host.act(action).then((message) => message && host.toast(message));
		});
	const rename = async () => {
		if (!editing) return;
		if (editing.text.trim() === "" || editing.text.trim() === session.name) {
			setEditing(null);
			return;
		}
		// The new name waits in the field until the hub can take it.
		if (!host.controls || !ready) return;
		if ((await host.controls.rename(editing.text)) === true) {
			setEditing(null);
			toast.show({ text: "Session renamed" });
		}
	};
	const error = state?.error && !(state.lastAction && MODEL_ACTIONS.has(state.lastAction)) ? state.error : null;
	return (
		<ScrollView
			automaticallyAdjustKeyboardInsets
			keyboardShouldPersistTaps="handled"
			style={{ backgroundColor: palette.canvas }}
			contentContainerStyle={{ paddingHorizontal: 16, paddingTop: 4, paddingBottom: 32, gap: 24 }}
		>
			<View style={{ gap: 4 }}>
				<Title
					name={session.name}
					editing={editing}
					canRename={capabilities.rename}
					ready={ready}
					startEditing={() => setEditing({ text: session.name, from: session.name })}
					edit={(text) => setEditing(editing ? { ...editing, text } : null)}
					submit={() => void rename()}
				/>
				<StateLine session={session} />
			</View>

			<Section title="Where">
				<Row symbol="server.rack" text={where.host} />
				{where.project ? <Row symbol="folder" text={where.project} /> : null}
				<Row mono text={wrapAfterSlashes(where.directory)} />
				{where.branch ? <Row symbol="arrow.triangle.branch" text={where.branch} /> : null}
			</Section>

			<Section title="Model">
				<Row
					symbol="cpu"
					text={host.modelLabel}
					accessibilityLabel={`Model, ${host.modelLabel}`}
					onPress={
						capabilities.changeModel
							? () => navigation.navigate("ModelSheet", { hubId, ref: sessionRef, setting: "model" })
							: undefined
					}
				/>
				{capabilities.changeVisionModel ? (
					<Row
						label="Vision model"
						text={visionModelLabel(session.visionModel, state?.catalog?.data)}
						onPress={() => navigation.navigate("ModelSheet", { hubId, ref: sessionRef, setting: "vision" })}
					/>
				) : null}
			</Section>

			{plugins ? (
				<Section
					title="Plugins"
					footer="Plugins are chosen when a session starts. To change them, start a new session or fork this one."
				>
					<Row text={plugins.line} />
					{plugins.names.length > 0 ? (
						<View style={{ paddingHorizontal: 16, paddingVertical: 8, gap: 2 }}>
							{plugins.names.map((name) => (
								<Quiet key={name}>{name}</Quiet>
							))}
						</View>
					) : null}
				</Section>
			) : null}

			{access ? (
				<Section title="Access">
					<Row label="Sandbox" text={access.mode} />
					<Row label="Network" text={access.network} />
				</Section>
			) : null}

			{Object.keys(usage).length > 0 ? (
				<Section title="Usage">
					{usage.tokens ? <Row text={usage.tokens} detail={usage.split} /> : null}
					{usage.cost ? <Row label="Cost" text={usage.cost} /> : null}
					{usage.workTime ? <Row label="Work time" text={usage.workTime} /> : null}
					{usage.context ? <ContextGauge text={usage.context.text} fraction={usage.context.fraction} /> : null}
					{usage.failedToolCalls ? <Row text={usage.failedToolCalls} tone="danger" /> : null}
				</Section>
			) : null}

			{session.goal || capabilities.goal ? (
				<Section title="Goal">
					{session.goal ? (
						<View style={{ paddingHorizontal: 16, paddingVertical: 12, gap: 4 }}>
							{session.goal.objective ? <Serif>{session.goal.objective}</Serif> : null}
							<Quiet tone={session.goal.status === "blocked" ? "attention" : "mid"}>
								{GOAL_STATUS[session.goal.status] ?? session.goal.status}
							</Quiet>
						</View>
					) : null}
					{capabilities.goal ? (
						<Row
							text={session.goal ? "Edit goal" : "Set a goal"}
							tone="accent"
							disabled={!ready}
							onPress={() => finish(() => {
								navigation.goBack();
								host.editGoal();
							})}
						/>
					) : null}
					{session.goal && capabilities.goal ? (
						<Row text="Clear goal" tone="accent" disabled={!ready} onPress={host.clearGoal} />
					) : null}
				</Section>
			) : null}

			{session.tasks?.total ? (
				<Section title="Tasks">
					{session.tasks.current ? <Row text={session.tasks.current.description} /> : null}
					<Row
						text={`Tasks · ${session.tasks.done} of ${session.tasks.total}`}
						onPress={() =>
							navigation.navigate("TasksSheet", {
								hubId,
								ref: sessionRef,
								threadId: session.threadId,
								hasTasks: true,
							})
						}
					/>
				</Section>
			) : null}

			{capabilities.sharedNotes ? (
				<Section title="Notes & links">
					<Row
						label="Notes & links"
						text={notesSummary(session)}
						onPress={() => navigation.navigate("NotesSheet", { hubId, ref: sessionRef })}
					/>
				</Section>
			) : null}

			<Section title="Actions" error={error}>
				{capabilities.forkFromTurn ? (
					<Row text="Aside" tone="accent" disabled={!ready} onPress={() => leaveFor("aside")} />
				) : null}
				{capabilities.forkFromTurn && forkPoint ? (
					<Row text="Fork from latest" tone="accent" disabled={!ready} onPress={() => leaveFor("fork")} />
				) : null}
				{capabilities.compact ? (
					<Row
						text="Compact context"
						tone="accent"
						disabled={!ready}
						onPress={() => void host.act("compact").then((message) => message && toast.show(message))}
					/>
				) : null}
				<Row text="Pin to category…" tone="accent" onPress={() => leaveFor("pin")} />
				<Row text="Archive" tone="accent" disabled={!ready} onPress={() => leaveFor("archive")} />
				{capabilities.shutdown && !shutDown ? (
					<Row
						text="Shut down"
						tone="danger"
						disabled={!ready}
						onPress={() => confirmShutDown(() => leaveFor("shutDown"))}
					/>
				) : null}
				{canDeleteSavedSession(session) ? (
					<Row text="Delete" tone="danger" onPress={() => leaveFor("delete")} />
				) : null}
			</Section>
		</ScrollView>
	);
}

function Title({
	name,
	editing,
	canRename,
	ready,
	startEditing,
	edit,
	submit,
}: {
	name: string;
	editing: { text: string } | null;
	canRename: boolean;
	ready: boolean;
	startEditing(): void;
	edit(text: string): void;
	submit(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const style = { color: palette.inkHi, fontSize: 20 * scale, lineHeight: 25 * scale, fontWeight: "600" as const };
	if (editing)
		return (
			<TextInput
				accessibilityLabel="Session name"
				autoFocus
				value={editing.text}
				onChangeText={edit}
				returnKeyType="done"
				onSubmitEditing={submit}
				allowFontScaling={allowFontScaling}
				style={{ ...style, minHeight: 44, borderBottomWidth: 1, borderColor: palette.accent }}
			/>
		);
	const text = (
		<Text accessibilityRole="header" allowFontScaling={allowFontScaling} style={style}>
			{name}
		</Text>
	);
	if (!canRename) return text;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${name}, rename`}
			accessibilityState={{ disabled: !ready }}
			disabled={!ready}
			onPress={startEditing}
			style={({ pressed }) => ({ minHeight: 44, justifyContent: "center", opacity: pressed ? 0.6 : 1 })}
		>
			{text}
		</Pressable>
	);
}

function StateLine({ session }: { session: MobileConversation }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const line = sessionStateLine(session, Date.now());
	const mark = markFor(line.state, false);
	return (
		<View style={{ flexDirection: "row", alignItems: "center", gap: 6 }}>
			{/* Never "meter": the sheet's mark is still; the check only narrows the type. */}
			{mark && mark !== "meter" ? (
				<SymbolView name={mark.name} tintColor={palette[mark.tint]} size={Math.min(mark.size, 15) * scale} />
			) : null}
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale, fontVariant: ["tabular-nums"] }}
			>
				{line.text}
			</Text>
		</View>
	);
}

/** A grouped section: its label, then its rows on the raised surface with
 * hairlines between them, then its footer or error line. */
function Section({
	title,
	footer,
	error,
	children,
}: {
	title: string;
	footer?: string;
	error?: string | null;
	children: ReactNode;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	// toArray drops the rows a section leaves out and keys the rest.
	const rows = Children.toArray(children) as ReactElement[];
	return (
		<View style={{ gap: 6 }}>
			<Text
				accessibilityRole="header"
				allowFontScaling={allowFontScaling}
				style={{
					paddingHorizontal: 16,
					fontSize: 12 * scale,
					lineHeight: 16 * scale,
					fontWeight: "600",
					letterSpacing: 12 * 0.06,
					textTransform: "uppercase",
					color: palette.inkMid,
				}}
			>
				{title}
			</Text>
			<View style={{ backgroundColor: palette.surface, borderRadius: 12, borderCurve: "continuous", overflow: "hidden" }}>
				{rows.map((row, index) => (
					<View
						key={row.key}
						style={index > 0 ? { borderTopWidth: StyleSheet.hairlineWidth, borderColor: palette.edge } : null}
					>
						{row}
					</View>
				))}
			</View>
			{footer ? (
				<View style={{ paddingHorizontal: 16 }}>
					<Quiet small>{footer}</Quiet>
				</View>
			) : null}
			{error ? (
				<Text
					accessibilityRole="alert"
					allowFontScaling={allowFontScaling}
					style={{ paddingHorizontal: 16, color: palette.dangerInk, fontSize: 13 * scale, lineHeight: 18 * scale }}
				>
					{error}
				</Text>
			) : null}
		</View>
	);
}

type Tone = "ink" | "accent" | "danger";

/** One row: a glyph, a label and its value, or one line of text. A row with
 * `onPress` is a button; one that opens something shows a chevron. */
function Row({
	symbol,
	label,
	text,
	detail,
	mono = false,
	tone = "ink",
	disabled = false,
	accessibilityLabel,
	onPress,
}: {
	symbol?: SFSymbol;
	label?: string;
	text: string;
	detail?: string;
	mono?: boolean;
	tone?: Tone;
	disabled?: boolean;
	accessibilityLabel?: string;
	onPress?: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const color = tone === "accent" ? palette.accentInk : tone === "danger" ? palette.dangerInk : label ? palette.inkMid : palette.inkHi;
	// An action row names itself; a row that opens a sheet shows where it leads.
	const opens = onPress !== undefined && tone === "ink";
	const content = (
		<>
			{symbol ? <SymbolView name={symbol} tintColor={palette.inkMid} size={17 * scale} /> : null}
			{label ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ flex: 1, color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale }}
				>
					{label}
				</Text>
			) : null}
			<View style={label ? { flexShrink: 1, alignItems: "flex-end" } : { flex: 1, gap: 2 }}>
				<Text
					allowFontScaling={allowFontScaling}
					style={
						mono
							? {
									fontFamily: typeRoles.machine.fontFamily,
									fontSize: typeRoles.machine.fontSize * scale,
									lineHeight: typeRoles.machine.lineHeight * scale,
									color: palette.inkHi,
								}
							: { color, fontSize: 17 * scale, lineHeight: 22 * scale, fontVariant: ["tabular-nums"] }
					}
				>
					{text}
				</Text>
				{detail ? (
					<Text
						allowFontScaling={allowFontScaling}
						style={{ color: palette.inkMid, fontSize: 13 * scale, lineHeight: 18 * scale, fontVariant: ["tabular-nums"] }}
					>
						{detail}
					</Text>
				) : null}
			</View>
			{opens ? <SymbolView name="chevron.right" tintColor={palette.inkLow} size={13 * scale} /> : null}
		</>
	);
	const layout = {
		minHeight: 44,
		flexDirection: "row" as const,
		alignItems: "center" as const,
		gap: 12,
		paddingHorizontal: 16,
		paddingVertical: 10,
	};
	if (!onPress) return <View style={layout}>{content}</View>;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={accessibilityLabel ?? (label ? `${label}, ${text}` : text)}
			accessibilityState={{ disabled }}
			disabled={disabled}
			onPress={onPress}
			style={({ pressed }) => ({
				...layout,
				opacity: disabled ? 0.4 : 1,
				backgroundColor: pressed ? palette.pressed : "transparent",
			})}
		>
			{content}
		</Pressable>
	);
}

/** Used of the window: a 4pt track with the used part filled. The hub reports
 * no compaction threshold, so there is no marker (ruling 36). */
function ContextGauge({ text, fraction }: { text: string; fraction: number }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ minHeight: 44, flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: 16 }}>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale }}
			>
				Context
			</Text>
			<View style={{ flex: 1, height: 4, borderRadius: 2, backgroundColor: palette.edge, overflow: "hidden" }}>
				<View
					testID="context-fill"
					style={{ width: `${Math.round(fraction * 1000) / 10}%`, height: 4, backgroundColor: palette.inkMid }}
				/>
			</View>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale, fontVariant: ["tabular-nums"] }}
			>
				{text}
			</Text>
		</View>
	);
}

function Serif({ children }: { children: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				fontFamily: typeRoles.yourMessage.fontFamily,
				fontSize: typeRoles.yourMessage.fontSize * scale,
				lineHeight: typeRoles.yourMessage.lineHeight * scale,
				color: palette.prose,
			}}
		>
			{children}
		</Text>
	);
}

/** Secondary text: 15/20 in ink-mid (or amber for a blocked goal), or 13/18
 * ink-low when `small`. */
function Quiet({
	children,
	small = false,
	tone = "mid",
}: {
	children: string;
	small?: boolean;
	tone?: "mid" | "attention";
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				fontSize: (small ? 13 : 15) * scale,
				lineHeight: (small ? 18 : 20) * scale,
				color: tone === "attention" ? palette.attentionInk : small ? palette.inkLow : palette.inkMid,
			}}
		>
			{children}
		</Text>
	);
}
