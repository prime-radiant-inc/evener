// The Notes & links sheet (spec 8.8): your note, the agent's note and the
// session's links, the web's Notes panel on the phone. A formSheet route that
// opens at large (sheetRoutes.ts). The session screen owns the notes
// controller and provides it here through notesHosts, so a save the sheet
// starts as it closes finishes after the sheet has gone.
import type { ThreadModel } from "@evener/appwire-client";
import { bindFilePath, cwdRelative, fileURLToPath } from "@evener/appwire-client/docContent";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import * as Clipboard from "expo-clipboard";
import { SymbolView } from "expo-symbols";
import * as WebBrowser from "expo-web-browser";
import { type ReactNode, useEffect, useRef, useState, useSyncExternalStore } from "react";
import {
	AccessibilityInfo,
	ActionSheetIOS,
	Alert,
	AppState,
	Platform,
	Pressable,
	ScrollView,
	Text,
	TextInput,
	View,
} from "react-native";
import { SwipeRow, swipeAccessibility } from "../board/SwipeRow";
import { scaledType, typeRoles } from "../design/tokens";
import { useReadingType } from "../display/displayContext";
import type { Routes } from "../screens";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetHosts, sheetKey, useSheetHost } from "../sheet/sheetHosts";
import { Toast, type ToastController, useToast } from "../Toast";
import { allowFontScaling, Copy, useColors, useTextScale } from "../ui";
import { wrapAfterSlashes } from "./format";
import { canWriteHumanNote, type NotesController, noteStatusLine, type SaveOutcome } from "./sessionNotes";
import { destructiveButton, haptic } from "../haptics";

export interface NotesHost {
	session: Pick<ThreadModel, "humanNote" | "agentNote" | "sessionUrls" | "status" | "resumeRequired" | "capabilities">;
	notes: NotesController;
	/** The sheet closed and its save settled: the screen confirms it. */
	saved(outcome: SaveOutcome): void;
	/** The session's folder, which a file link must be inside to open. */
	cwd: string;
	/** The session's title as its screen shows it, where a review goes. */
	title: string;
}

export const notesHosts = sheetHosts<NotesHost>();

export function NotesSheet({ route, navigation }: NativeStackScreenProps<Routes, "NotesSheet">) {
	const { hubId, ref, focusEditor = false } = route.params;
	const toast = useToast();
	const live = useRef<NotesHost | undefined>(undefined);
	// Closing saves (ruling 37): Done and a swipe down both land here, and
	// nothing in the sheet is unsaved input to ask about.
	const sheet = useSheet({
		onClosed: () => {
			const host = live.current;
			// A session that can't take notes keeps anything unsaved on this
			// phone rather than sending it.
			if (host && canWriteHumanNote(host.session)) void host.notes.flush().then(host.saved);
		},
	});
	const host = useSheetHost(notesHosts, sheetKey(hubId, ref), sheet);
	// Keep the last real host once the screen's gone: its own release can
	// land before this sheet's unmount does (a real goBack()'s ordering), and
	// onClosed still needs something to flush through. An effect, not a
	// render-time assignment, so a re-render React discards without
	// committing never overwrites it with a host that was never actually shown.
	useEffect(() => {
		if (host) live.current = host;
	}, [host]);
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => {
			const current = live.current;
			if (state === "background" && current && canWriteHumanNote(current.session)) void current.notes.flush();
		});
		return () => subscription.remove();
	}, []);
	if (!host) return null;
	return (
		<Sheet
			title="Notes & links"
			done={{ onPress: () => sheet.finish() }}
			accessory={<Toast toast={toast.toast} dismiss={toast.dismiss} />}
		>
			<ScrollView
				automaticallyAdjustKeyboardInsets
				keyboardShouldPersistTaps="handled"
				keyboardDismissMode="on-drag"
				contentContainerStyle={{ paddingHorizontal: 20, paddingTop: 8, paddingBottom: 24, gap: 24 }}
			>
				<NotesBody
					host={host}
					focusEditor={focusEditor}
					toast={toast}
					// A document opens in the Reader over the session, once the
					// sheet has gone (ruling 26); closing saves the note as usual.
					openDocument={(path) => {
						const reference = bindFilePath(path, host.cwd);
						if (!reference) return;
						sheet.finish(() => {
							navigation.goBack();
							navigation.navigate("Reader", {
								hubId,
								sessionRef: ref,
								path: reference.path,
								reference,
								sessionTitle: host.title,
							});
						});
					}}
				/>
			</ScrollView>
		</Sheet>
	);
}

function NotesBody({
	host,
	focusEditor,
	toast,
	openDocument,
}: {
	host: NotesHost;
	focusEditor: boolean;
	toast: ToastController;
	openDocument(path: string): void;
}) {
	const { session } = host;
	const writable = canWriteHumanNote(session);
	const human = session.humanNote.trim();
	// Removing a link succeeds on the hub before the session's own re-read lands
	// (and counts as removed when the hub says it is already gone), so the row
	// drops from this list locally rather than lingering until the next read
	// (RoboRev #2769).
	const [removed, setRemoved] = useState<ReadonlySet<string>>(() => new Set());
	const links = session.sessionUrls.filter((link) => !removed.has(link.id));
	// A re-read that drops the row, or re-adds one under an id we hid, must not
	// be masked by a stale id: keep only ids the session still lists.
	useEffect(() => {
		setRemoved((previous) => {
			if (previous.size === 0) return previous;
			const listed = new Set(session.sessionUrls.map((link) => link.id));
			const next = new Set([...previous].filter((id) => listed.has(id)));
			return next.size === previous.size ? previous : next;
		});
	}, [session.sessionUrls]);
	if (!writable && !human && !session.agentNote.trim() && links.length === 0) return <Copy muted>No shared notes</Copy>;
	return (
		<>
			{writable ? (
				<Group title="Your note">
					<NoteEditor notes={host.notes} working={session.status.type === "active"} focus={focusEditor} />
				</Group>
			) : human ? (
				<Group title="Your note">
					<Copy variant="yourMessage">{session.humanNote}</Copy>
				</Group>
			) : null}
			<Group title="Agent">
				{session.agentNote.trim() ? (
					<Copy variant="yourMessage">{session.agentNote}</Copy>
				) : (
					<Quiet>No agent note yet</Quiet>
				)}
			</Group>
			<Group title="Links">
				{links.length === 0 ? <Quiet>No links yet</Quiet> : null}
				{links.map((link) => (
					<LinkRow
						key={link.id}
						link={link}
						writable={writable}
						notes={host.notes}
						toast={toast}
						cwd={host.cwd}
						openDocument={openDocument}
						onRemoved={() => setRemoved((previous) => new Set(previous).add(link.id))}
					/>
				))}
				{writable && links.length > 0 ? (
					<Quiet small>The agent adds links as it works. Swipe left on one to remove it.</Quiet>
				) : null}
			</Group>
		</>
	);
}

function Group({ title, children }: { title: string; children: ReactNode }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ gap: 8 }}>
			<Text
				accessibilityRole="header"
				allowFontScaling={allowFontScaling}
				style={{
					fontSize: 13 * scale,
					lineHeight: 18 * scale,
					fontWeight: "600",
					color: palette.inkMid,
					textTransform: "uppercase",
				}}
			>
				{title}
			</Text>
			{children}
		</View>
	);
}

/** Secondary text: 15/20 inkMid, or 13/18 inkLow when `small`. */
function Quiet({ children, small = false }: { children: string; small?: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				fontSize: (small ? 13 : 15) * scale,
				lineHeight: (small ? 18 : 20) * scale,
				color: small ? palette.inkLow : palette.inkMid,
			}}
		>
			{children}
		</Text>
	);
}

function NoteEditor({ notes, working, focus }: { notes: NotesController; working: boolean; focus: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const reading = useReadingType();
	const note = useSyncExternalStore(notes.subscribe, notes.getSnapshot);
	const [focused, setFocused] = useState(false);
	// The status line under the editor. iOS ignores accessibilityLiveRegion, so
	// announce each change there; Android reads the polite region on its own, and
	// announcing too would say it twice. The first render is skipped: the line
	// starts as the note's standing explanation, not a change to speak (#2903).
	const status = noteStatusLine(note.phase, working);
	const announced = useRef(status);
	useEffect(() => {
		if (status === announced.current) return;
		announced.current = status;
		if (Platform.OS === "ios") AccessibilityInfo.announceForAccessibility(status);
	}, [status]);
	// Opened from the bar's "Your note: …", the caret waits at the end, ready to
	// add to it; the first move of the caret hands the selection back to iOS.
	const [caret, setCaret] = useState(() => (focus ? { start: note.text.length, end: note.text.length } : undefined));
	const ring = focused ? 2 : 1;
	return (
		<View style={{ gap: 8 }}>
			<TextInput
				accessibilityLabel="Your note"
				multiline
				value={note.text}
				// No native maxLength: it counts UTF-16 units, not the daemon's
				// runes. notes.edit() enforces NOTE_LIMIT by code point on every
				// change instead (RoboRev #2769 round 3).
				placeholder="Make a note…"
				placeholderTextColor={palette.inkLow}
				autoFocus={focus}
				selection={caret}
				onSelectionChange={() => setCaret(undefined)}
				onChangeText={(text) => notes.edit(text)}
				onFocus={() => {
					setFocused(true);
					notes.focus();
				}}
				onBlur={() => {
					setFocused(false);
					notes.blur();
				}}
				allowFontScaling={allowFontScaling}
				style={{
					fontFamily: reading.yourMessage.fontFamily,
					fontSize: reading.yourMessage.fontSize * scale,
					lineHeight: reading.yourMessage.lineHeight * scale,
					color: palette.prose,
					minHeight: 120,
					textAlignVertical: "top",
					// The ring grows inward, so the text never moves.
					padding: 13 - ring,
					borderWidth: ring,
					borderColor: focused ? palette.accent : palette.edgeStrong,
					borderRadius: 12,
					backgroundColor: palette.surface,
				}}
			/>
			<Text
				allowFontScaling={allowFontScaling}
				accessibilityLiveRegion="polite"
				style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
			>
				{status}
			</Text>
		</View>
	);
}

type LinkKind = "web" | "file" | "other";

function linkKind(url: string): LinkKind {
	if (/^https?:\/\//i.test(url)) return "web";
	if (/^file:\/\//i.test(url)) return "file";
	return "other";
}

function LinkRow({
	link,
	writable,
	notes,
	toast,
	cwd,
	openDocument,
	onRemoved,
}: {
	link: ThreadModel["sessionUrls"][number];
	writable: boolean;
	notes: NotesController;
	toast: ToastController;
	cwd: string;
	openDocument(path: string): void;
	onRemoved(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const kind = linkKind(link.url);
	const label = link.label?.trim();
	// A file link opens in the Reader only when it names a file inside the
	// session's folder, the only place the hub serves documents from. Another
	// machine, a malformed escape or a file elsewhere keeps its text, as on
	// the web. Other schemes don't open.
	const absolutePath = kind === "file" ? fileURLToPath(link.url) : undefined;
	const document = absolutePath === undefined ? undefined : cwdRelative(absolutePath, cwd);
	const open = () =>
		void WebBrowser.openBrowserAsync(link.url, {
			dismissButtonStyle: "done",
			controlsColor: palette.accentInk,
		}).catch(() => toast.show({ text: "Couldn't open that link." }));
	const press =
		kind === "web"
			? open
			: document !== undefined && absolutePath !== undefined
				? () => openDocument(absolutePath)
				: undefined;
	const remove = () =>
		void notes.removeLink(link.id).then((removed) => {
			if (removed) {
				onRemoved();
				toast.show({ text: "Link removed. Only the agent can add links." });
			} else {
				toast.show({ text: "Couldn't remove that link." });
			}
		});
	// VoiceOver names the swipe's remove in full; the panel has room for one word.
	const removeAction = { key: "remove", label: "Remove link", run: remove };
	const menu = () => {
		const items = [
			...(kind === "web" ? [{ label: "Open", run: open }] : []),
			{ label: "Copy link", run: () => void Clipboard.setStringAsync(link.url) },
			...(writable ? [{ label: "Remove link", run: remove }] : []),
		];
		if (Platform.OS === "ios") {
			ActionSheetIOS.showActionSheetWithOptions(
				{
					options: [...items.map((item) => item.label), "Cancel"],
					cancelButtonIndex: items.length,
					// Remove link, when offered, is the last item before Cancel.
					...(writable ? { destructiveButtonIndex: items.length - 1 } : {}),
				},
				(index) => {
					// Spec 16.6: Remove link is its own confirmation.
					if (writable && index === items.length - 1) haptic("rigid");
					items[index]?.run();
				},
			);
			return;
		}
		// Android's alert dismisses by a tap outside rather than spending one
		// of its buttons on Cancel (TimelineItem.tsx's showMenu).
		Alert.alert(
			label || link.url,
			undefined,
			items.map((item) =>
				item.run === remove ? destructiveButton(item.label, item.run) : { text: item.label, onPress: item.run },
			),
			{ cancelable: true },
		);
	};
	const row = (
		<Pressable
			accessibilityRole={press ? "link" : "text"}
			accessibilityLabel={label ? `${label}, ${link.url}` : link.url}
			onPress={press}
			onLongPress={menu}
			// VoiceOver's stand-in for the swipe.
			{...(writable ? swipeAccessibility(undefined, [removeAction]) : {})}
			style={({ pressed }) => ({
				minHeight: 44,
				flexDirection: "row",
				alignItems: "flex-start",
				gap: 10,
				paddingVertical: 6,
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<SymbolView
				name={kind === "file" ? "doc.text" : "globe"}
				size={15 * scale}
				tintColor={palette.inkMid}
				style={{ marginTop: 2 }}
			/>
			<View style={{ flex: 1, gap: 2 }}>
				{label ? (
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
					>
						{label}
					</Text>
				) : null}
				<Text
					allowFontScaling={allowFontScaling}
					style={{ ...scaledType(typeRoles.machine, scale), color: palette.inkMid }}
				>
					{wrapAfterSlashes(link.url)}
				</Text>
			</View>
		</Pressable>
	);
	return writable ? (
		<SwipeRow destructive={{ ...removeAction, label: "Remove" }} backdrop={palette.canvas}>
			{row}
		</SwipeRow>
	) : (
		row
	);
}
