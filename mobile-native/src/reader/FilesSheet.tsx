// Files & artifacts (spec 10.1, ruling 26): every document the session wrote
// or linked, newest write first, each with a blue dot while it's new or
// changed since you last opened it. The list is the one the sheet opened
// with (its params are plain data), so it needs no host. Tapping a row closes
// the sheet, then opens the document in the Reader over the session.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { bindFilePath } from "../../../appwire-client/typescript/fileReferences";
import { FlatList, Pressable, Text, View } from "react-native";
import { fonts } from "../design/tokens";
import type { Routes } from "../screens";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useReadingFace } from "../display/displayContext";
import { useDocumentFacts } from "./DocumentChip";
import { FreshDot } from "./FreshDot";
import type { SessionDocument } from "./sessionDocuments";

export function FilesSheet({ route, navigation }: NativeStackScreenProps<Routes, "FilesSheet">) {
	const { hubId, ref, title, cwd, documents } = route.params;
	const sheet = useSheet();
	const { palette } = useColors();
	const scale = useTextScale();
	const open = ({ path, updatedAt }: SessionDocument) => {
		const reference = bindFilePath(path, cwd);
		if (!reference) return;
		sheet.finish(() => {
			navigation.goBack();
			navigation.navigate("Reader", {
				hubId,
				sessionRef: ref,
				path: reference.path,
				reference,
				sessionTitle: title,
				...(updatedAt === undefined ? {} : { updatedAt }),
			});
		});
	};
	return (
		<Sheet title="Files & artifacts" done={{ onPress: () => sheet.finish() }}>
			<FlatList
				data={documents}
				keyExtractor={(document) => document.path}
				contentContainerStyle={{ paddingVertical: 8 }}
				ListEmptyComponent={
					<Text
						allowFontScaling={allowFontScaling}
						style={{ padding: 16, color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale }}
					>
						This session hasn't written or linked any documents yet.
					</Text>
				}
				renderItem={({ item }) => (
					<DocumentRow hubId={hubId} sessionRef={ref} document={item} onPress={() => open(item)} />
				)}
			/>
		</Sheet>
	);
}

function DocumentRow({
	hubId,
	sessionRef,
	document,
	onPress,
}: {
	hubId: string;
	sessionRef: string;
	document: SessionDocument;
	onPress(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const face = useReadingFace();
	const { kind, title, lines, age, spokenAge, freshness } = useDocumentFacts(
		hubId,
		sessionRef,
		document.path,
		document.updatedAt,
	);
	const fresh = freshness === "read" ? null : freshness;
	const facts = [lines, age].filter((fact) => fact !== null);
	const label = [kind, title, document.path, lines, spokenAge, fresh].filter((part) => part !== null).join(", ");
	const small = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };
	return (
		<Pressable
			testID="document-row"
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				paddingVertical: 10,
				paddingHorizontal: 16,
				gap: 2,
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<View style={{ flexDirection: "row", alignItems: "baseline", gap: 8 }}>
				{fresh ? <FreshDot /> : null}
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 13 * scale, lineHeight: 20 * scale, fontWeight: "600", color: palette.inkMid }}
				>
					{kind}
				</Text>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
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
			<View style={{ flexDirection: "row", alignItems: "baseline" }}>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					ellipsizeMode="middle"
					style={{ ...small, flexShrink: 1, fontFamily: fonts.mono, fontSize: 12 * scale }}
				>
					{document.path}
				</Text>
				{facts.length > 0 ? (
					<Text allowFontScaling={allowFontScaling} style={{ ...small, fontVariant: ["tabular-nums"] }}>
						{facts.map((fact) => ` · ${fact}`).join("")}
					</Text>
				) : null}
			</View>
		</Pressable>
	);
}
