// The Session sheet (spec 8.6): everything the session knows about itself,
// and what you can do to it. A formSheet route that opens at large
// (sheetRoutes.ts). The session screen provides its live session, controls
// and actions through sessionInfoHosts (ruling 37); the sheet shows only
// facts the thread carries (ruling 21, with S15's access).
import { useNavigation } from "@react-navigation/native";
import type { NativeStackNavigationProp, NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { useState } from "react";
import { Alert, Pressable, Text, TextInput, View } from "react-native";
import { markFor } from "../board/StateMark";
import { space } from "../design/tokens";
import { useReadingType } from "../display/displayContext";
import type { MobileConversation } from "../projectedRows";
import type { Routes } from "../screens";
import { type SessionControls, useControlsState } from "../sessionControls";
import { Group, GroupFooter, GroupedPage, Row } from "../sheet/Grouped";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetHosts, sheetKey, useSheetHost } from "../sheet/sheetHosts";
import { Toast, type ToastController, type ToastMessage, useToast } from "../Toast";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { wrapAfterSlashes } from "./format";
import { MODEL_ACTIONS } from "./ModelSheet";
import {
	accessFacts,
	canDeleteSavedSession,
	canOpenModelSheet,
	hostIdOf,
	latestForkPoint,
	notesSummary,
	pluginsLine,
	type SessionHost,
	usageFacts,
	visionModelLabel,
	whereFacts,
} from "./sessionFacts";
import { SHUT_DOWN, sessionStateLine } from "./sessionState";
import { destructiveButton } from "../haptics";

export type SessionInfoAction = "aside" | "fork" | "compact" | "pin" | "archive" | "shutDown" | "delete";

export interface SessionInfoHost {
	session: MobileConversation;
	/** Null while the hub is away: the sheet still shows what it knows. */
	controls: SessionControls | null;
	/** A host's name and whether it's connected (sessionHosts). */
	host(hostId: string): SessionHost;
	/** The model and its effort, as the composer's chip names them. */
	modelLabel: string;
	/** How long a subagent has run at `now`, as its row times it; null for a
	 * session that isn't one, which the turn's own age times instead. */
	runMs(now: number): number | null;
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
		destructiveButton("Shut down", () => shutDown()),
	]);
}

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
	const navigation = useNavigation<NativeStackNavigationProp<Routes>>();
	const state = useControlsState(host.controls);
	const { session, ready } = host;
	const capabilities = session.capabilities;
	const sessionHost = host.host(hostIdOf(session.ref));
	const where = whereFacts(session, () => sessionHost.label);
	const plugins = pluginsLine(session);
	const access = accessFacts(session);
	const usage = usageFacts(session);
	const forkPoint = latestForkPoint(session.items);
	const shutDown = SHUT_DOWN.has(session.status.type);
	const opensModel = canOpenModelSheet(session);
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
		<GroupedPage>
			<View style={{ paddingHorizontal: space.margin, paddingTop: 4, gap: 4 }}>
				<Title
					name={session.name}
					editing={editing}
					canRename={capabilities.rename}
					ready={ready}
					startEditing={() => setEditing({ text: session.name, from: session.name })}
					edit={(text) => setEditing(editing ? { ...editing, text } : null)}
					submit={() => void rename()}
				/>
				<StateLine session={session} runMs={host.runMs} />
			</View>

			<Group label="Where">
				<Row
					icon="server.rack"
					label={where.host}
					value={sessionHost.online === null ? undefined : <ConnectionDot online={sessionHost.online} />}
					accessibilityLabel={
						sessionHost.online === null ? where.host : `${where.host}, ${sessionHost.online ? "connected" : "offline"}`
					}
				/>
				{where.project ? <Row icon="folder" label={where.project} /> : null}
				<Row machineLabel label={wrapAfterSlashes(where.directory)} accessibilityLabel={where.directory} />
				{where.branch ? <Row icon="arrow.triangle.branch" label={where.branch} /> : null}
			</Group>

			<Group label="Model">
				<Row
					icon="cpu"
					label={host.modelLabel}
					accessibilityLabel={`Model, ${host.modelLabel}`}
					chevron={opensModel}
					onPress={
						opensModel
							? () => navigation.navigate("ModelSheet", { hubId, ref: sessionRef, setting: "model" })
							: undefined
					}
				/>
				{capabilities.changeVisionModel ? (
					<Row
						label="Vision model"
						value={visionModelLabel(session.visionModel, state?.catalog?.data)}
						chevron
						onPress={() => navigation.navigate("ModelSheet", { hubId, ref: sessionRef, setting: "vision" })}
					/>
				) : null}
			</Group>

			{plugins ? (
				<>
					<Group label="Plugins">
						<Row label={plugins.line} />
						{plugins.names.length > 0 ? (
							<View style={{ paddingHorizontal: 16, paddingVertical: 8, gap: 2 }}>
								{plugins.names.map((name) => (
									<Quiet key={name}>{name}</Quiet>
								))}
							</View>
						) : null}
					</Group>
					<GroupFooter>
						Plugins are chosen when a session starts. To change them, start a new session or fork this one.
					</GroupFooter>
				</>
			) : null}

			{access ? (
				<Group label="Access">
					<Row label="Sandbox" value={access.mode} />
					<Row label="Network" value={access.network} />
				</Group>
			) : null}

			{Object.keys(usage).length > 0 ? (
				<Group label="Usage">
					{usage.tokens ? <Row label={usage.tokens} sub={usage.split} /> : null}
					{usage.cost ? <Row label="Cost" value={usage.cost} /> : null}
					{usage.workTime ? <Row label="Work time" value={usage.workTime} /> : null}
					{usage.context ? <ContextGauge text={usage.context.text} fraction={usage.context.fraction} /> : null}
					{usage.failedToolCalls ? <Row label={usage.failedToolCalls} tone="danger" /> : null}
				</Group>
			) : null}

			{session.goal || capabilities.goal ? (
				<Group label="Goal">
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
							label={session.goal ? "Edit goal" : "Set a goal"}
							tone="accent"
							disabled={!ready}
							onPress={() =>
								finish(() => {
									navigation.goBack();
									host.editGoal();
								})
							}
						/>
					) : null}
					{session.goal && capabilities.goal ? (
						<Row label="Clear goal" tone="accent" disabled={!ready} onPress={host.clearGoal} />
					) : null}
				</Group>
			) : null}

			{session.tasks?.total ? (
				<Group label="Tasks">
					{session.tasks.current ? <Row label={session.tasks.current.description} /> : null}
					<Row
						label={`Tasks · ${session.tasks.done} of ${session.tasks.total}`}
						chevron
						onPress={() =>
							navigation.navigate("TasksSheet", {
								hubId,
								ref: sessionRef,
								threadId: session.threadId,
								hasTasks: true,
							})
						}
					/>
				</Group>
			) : null}

			{capabilities.sharedNotes ? (
				<Group label="Notes & links">
					<Row
						label="Notes & links"
						value={notesSummary(session)}
						chevron
						onPress={() => navigation.navigate("NotesSheet", { hubId, ref: sessionRef })}
					/>
				</Group>
			) : null}

			<Group label="Actions">
				{capabilities.forkFromTurn ? (
					<Row label="Aside" tone="accent" disabled={!ready} onPress={() => leaveFor("aside")} />
				) : null}
				{capabilities.forkFromTurn && forkPoint ? (
					<Row label="Fork from latest" tone="accent" disabled={!ready} onPress={() => leaveFor("fork")} />
				) : null}
				{capabilities.compact ? (
					<Row
						label="Compact context"
						tone="accent"
						disabled={!ready}
						onPress={() => void host.act("compact").then((message) => message && toast.show(message))}
					/>
				) : null}
				<Row label="Pin to category…" tone="accent" onPress={() => leaveFor("pin")} />
				<Row label="Archive" tone="accent" disabled={!ready} onPress={() => leaveFor("archive")} />
				{capabilities.shutdown && !shutDown ? (
					<Row
						label="Shut down"
						tone="danger"
						disabled={!ready}
						onPress={() => confirmShutDown(() => leaveFor("shutDown"))}
					/>
				) : null}
				{canDeleteSavedSession(session) ? (
					<Row label="Delete" tone="danger" onPress={() => leaveFor("delete")} />
				) : null}
			</Group>
			{error ? (
				// The shared GroupFooter is a plain Text; keep the alert so VoiceOver
				// still announces a failed action.
				<View accessible accessibilityRole="alert">
					<GroupFooter tone="danger">{error}</GroupFooter>
				</View>
			) : null}
		</GroupedPage>
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

/** A host's connection dot (spec 8.6): alive while it's connected. */
function ConnectionDot({ online }: { online: boolean }) {
	const { palette } = useColors();
	return <SymbolView name="circle.fill" size={8} tintColor={online ? palette.alive : palette.inkLow} />;
}

function StateLine({ session, runMs }: { session: MobileConversation; runMs(now: number): number | null }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const now = Date.now();
	const line = sessionStateLine(session, now, runMs(now));
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

/** Used of the window: a 4pt track with the used part filled. The hub reports
 * no compaction threshold, so there is no marker (ruling 36). */
function ContextGauge({ text, fraction }: { text: string; fraction: number }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			accessible
			accessibilityLabel={`Context, ${text}`}
			style={{ minHeight: 44, flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: 16 }}
		>
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
	const reading = useReadingType();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				fontFamily: reading.yourMessage.fontFamily,
				fontSize: reading.yourMessage.fontSize * scale,
				lineHeight: reading.yourMessage.lineHeight * scale,
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
