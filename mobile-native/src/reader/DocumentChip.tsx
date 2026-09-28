// A document the agent named, under its message (spec 8.2, ruling 28): the
// document's kind and title, then its file name, its length and, when the
// session wrote it, how long ago. Tapping it opens the Reader. Before its
// summary lands the title is the file name, and nothing else on it moves.
import { filenameOf } from "@evener/appwire-client/docContent";
import { useMemo } from "react";
import { Pressable, Text, View } from "react-native";
import { fonts } from "../design/tokens";
import { compactDuration, spokenDuration } from "../session/format";
import { useMinuteClock } from "../session/minuteClock";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { documentKind } from "./documentSource";
import { messageDocuments } from "./documentReferences";
import { useDocumentSummary } from "./useDocumentSummary";

export interface DocumentChipProps {
	hubId: string;
	sessionRef: string;
	path: string;
	/** When the session last wrote the file; absent for a file it only named. */
	updatedAt?: string;
	onOpen(): void;
}

export function DocumentChip({ hubId, sessionRef, path, updatedAt, onOpen }: DocumentChipProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const now = useMinuteClock();
	const summary = useDocumentSummary(hubId, sessionRef, path, updatedAt);
	const kind = documentKind(path);
	const name = filenameOf(path);
	const title = summary?.title || name;
	const lines = summary?.lines === undefined ? null : `${summary.lines} ${summary.lines === 1 ? "line" : "lines"}`;
	const written = updatedAt === undefined ? Number.NaN : Date.parse(updatedAt);
	const age = Number.isNaN(written) ? null : now - written;
	const facts = [lines, age === null ? null : `${compactDuration(age)} ago`].filter((fact) => fact !== null);
	const label = [kind, title, name, lines, age === null ? null : `${spokenDuration(age)} ago`]
		.filter((part) => part !== null)
		.join(", ");
	const small = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={onOpen}
			style={({ pressed }) => ({
				marginHorizontal: 16,
				marginTop: 8,
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
						fontFamily: fonts.serifSemibold,
						fontSize: 15 * scale,
						lineHeight: 20 * scale,
						color: palette.inkHi,
					}}
				>
					{title}
				</Text>
			</View>
			<Text allowFontScaling={allowFontScaling} numberOfLines={1} style={{ ...small, fontVariant: ["tabular-nums"] }}>
				<Text style={{ fontFamily: fonts.mono, fontSize: 12 * scale }}>{name}</Text>
				{facts.map((fact) => ` · ${fact}`).join("")}
			</Text>
		</Pressable>
	);
}

/** The chips under one agent's message: the documents it names inside the
 * session's folder, each once, in reading order. `writes` is when the session
 * last wrote each file (fileWrites), which gives a chip its age. */
export function MessageDocuments({
	hubId,
	sessionRef,
	markdown,
	cwd,
	writes,
	open,
}: {
	hubId: string;
	sessionRef: string;
	markdown: string;
	cwd: string;
	writes: ReadonlyMap<string, string>;
	open(path: string, updatedAt: string | undefined): void;
}) {
	const paths = useMemo(() => messageDocuments(markdown, cwd, writes), [markdown, cwd, writes]);
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
