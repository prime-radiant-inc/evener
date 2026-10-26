// A document the agent named, under its message (spec 8.2, ruling 28): the
// document's kind and title, then its file name, its length and, when the
// session wrote it, how long ago. Tapping it opens the Reader. Before its
// summary lands the title is the file name, and nothing else on it moves.
import { filenameOf } from "@evener/appwire-client/docContent";
import { useMemo, useSyncExternalStore } from "react";
import { Pressable, Text, View } from "react-native";
import { spokenDuration } from "@evener/appwire-client";
import { fonts } from "../design/tokens";
import { timeAgo } from "../session/format";
import { useMinuteClock } from "../session/minuteClock";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useReadingFace } from "../display/displayContext";
import { menuAccessibility, menuPreview, showMenu } from "../longPressMenu";
import { documentKind } from "./documentSource";
import { FreshDot } from "./FreshDot";
import { messageDocuments } from "./documentReferences";
import { documentMemory } from "./nativeDocumentMemory";
import { documentFreshness } from "./sessionDocuments";
import { useDocumentSummary } from "./useDocumentSummary";
import { useShareDocument } from "./useShareDocument";

export interface DocumentChipProps {
	hubId: string;
	sessionRef: string;
	path: string;
	/** When the session last wrote the file; absent for a file it only named. */
	updatedAt?: string;
	onOpen(): void;
}

/** What a chip and a Files row say about a document: its kind, its title
 * (the file name until its summary lands), its length, its age when the
 * session wrote it, and whether it's new or changed since you last read it. */
export function useDocumentFacts(hubId: string, sessionRef: string, path: string, updatedAt?: string) {
	const now = useMinuteClock();
	const summary = useDocumentSummary(hubId, sessionRef, path, updatedAt);
	const memory = documentMemory(hubId);
	useSyncExternalStore(memory.subscribe, memory.getRevision);
	const name = filenameOf(path);
	const written = updatedAt === undefined ? Number.NaN : Date.parse(updatedAt);
	const age = Number.isNaN(written) ? null : now - written;
	return {
		kind: documentKind(path),
		title: summary?.title || name,
		name,
		lines: summary?.lines === undefined ? null : `${summary.lines} ${summary.lines === 1 ? "line" : "lines"}`,
		age: age === null ? null : timeAgo(age),
		spokenAge: age === null ? null : timeAgo(age, spokenDuration),
		freshness: documentFreshness(memory.lastRead({ sessionRef, path }), updatedAt),
	};
}

export function DocumentChip({ hubId, sessionRef, path, updatedAt, onOpen }: DocumentChipProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const face = useReadingFace();
	const { kind, title, name, lines, age, spokenAge, freshness } = useDocumentFacts(hubId, sessionRef, path, updatedAt);
	const shareDocument = useShareDocument(hubId, sessionRef);
	// A chip marks only a change (8.2); Files and its chip mark what's new. On
	// screen the change is the blue dot alone; VoiceOver, which can't see the
	// dot, says it in words.
	const changed = freshness === "changed";
	const facts = [lines, age].filter((fact) => fact !== null);
	// Until its summary lands the title is the file name, so a second copy
	// would name it twice on screen and in VoiceOver.
	const secondary = title === name ? null : name;
	const spokenChange = changed ? "changed since you last read" : null;
	const label = [kind, title, secondary, lines, spokenAge, spokenChange].filter((part) => part !== null).join(", ");
	const details = secondary === null ? facts.join(" · ") : facts.map((fact) => ` · ${fact}`).join("");
	const small = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };
	// A chip is where the agent's video or PDF first appears, so it offers the
	// same system sheet the Reader does. The hook reports a failure itself.
	// Memoized because a chip is a transcript row that re-renders on every
	// minute tick, and its Pressable props should stay stable between them.
	const menu = useMemo(
		() => [{ name: "share", label: "Share", run: () => shareDocument(path) }],
		[shareDocument, path],
	);
	const accessibility = useMemo(() => menuAccessibility(menu), [menu]);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={onOpen}
			onLongPress={() => showMenu(menu, menuPreview(title))}
			{...accessibility}
			// The list's 16pt sides and the row's 8pt gap place it (spec 8.2).
			style={({ pressed }) => ({
				minHeight: 44,
				paddingVertical: 12,
				paddingHorizontal: 14,
				borderRadius: 12,
				backgroundColor: palette.inset,
				gap: 2,
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<View style={{ flexDirection: "row", alignItems: "baseline", gap: 8 }}>
				{changed ? <FreshDot /> : null}
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 12 * scale, lineHeight: 20 * scale, fontWeight: "600", color: palette.inkMid }}
				>
					{kind}
				</Text>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					ellipsizeMode="tail"
					style={{
						flexShrink: 1,
						...face.semibold,
						fontSize: 15 * scale,
						lineHeight: 20 * scale,
						color: palette.inkHi,
					}}
				>
					{title}
				</Text>
			</View>
			{/* The second line is always there, held by a no-break space until its
			    summary or age lands, so the chip never grows and moves the rows
			    below it. */}
			<Text allowFontScaling={allowFontScaling} numberOfLines={1} style={{ ...small, fontVariant: ["tabular-nums"] }}>
				{secondary === null ? null : <Text style={{ fontFamily: fonts.mono, fontSize: 12 * scale }}>{secondary}</Text>}
				{secondary === null && facts.length === 0 ? "\u00a0" : details}
			</Text>
		</Pressable>
	);
}

/** The chips under one agent's message: the documents it names inside the
 * session's folder, each once, in reading order. `writes` is when the session
 * last wrote each file (fileWrites), which gives a chip its age; `written` is
 * the paths the session wrote, which lets a bare name become a chip. */
export function MessageDocuments({
	hubId,
	sessionRef,
	markdown,
	cwd,
	writes,
	written,
	open,
}: {
	hubId: string;
	sessionRef: string;
	markdown: string;
	cwd: string;
	writes: ReadonlyMap<string, string>;
	/** The paths the session wrote, whatever their write time. */
	written: ReadonlySet<string>;
	open(path: string, updatedAt: string | undefined): void;
}) {
	const paths = useMemo(() => messageDocuments(markdown, cwd, written), [markdown, cwd, written]);
	return paths.map((path) => {
		const updatedAt = writes.get(path);
		return (
			<DocumentChip
				key={path}
				hubId={hubId}
				sessionRef={sessionRef}
				path={path}
				{...(updatedAt === undefined ? {} : { updatedAt })}
				onOpen={() => open(path, updatedAt)}
			/>
		);
	});
}
