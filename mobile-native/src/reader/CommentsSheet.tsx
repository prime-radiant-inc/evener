// A document's comments (spec 10.2, ruling 26): each with its quote and its
// text, a way to show its block in the Reader, and Delete. They are drafts on
// this phone until the review goes out, so Delete doesn't ask. Send review, at
// the foot of the list, opens the Review sheet over this one.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useSyncExternalStore } from "react";
import { FlatList, Pressable, Text, View } from "react-native";
import type { Routes } from "../screens";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetKey, useSheetHost } from "../sheet/sheetHosts";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { QuoteBlock } from "./CommentSheet";
import type { DocumentComment } from "./documentMemory";
import { documentMemory } from "./nativeDocumentMemory";
import { readerHosts } from "./readerHosts";

export function CommentsSheet({ route, navigation }: NativeStackScreenProps<Routes, "CommentsSheet">) {
	const { hubId, sessionRef, path } = route.params;
	const sheet = useSheet();
	const host = useSheetHost(readerHosts, sheetKey(hubId, sessionRef, path), sheet);
	const memory = documentMemory(hubId);
	const key = { sessionRef, path };
	useSyncExternalStore(memory.subscribe, memory.getRevision);
	const comments = memory.comments(key);
	const { palette } = useColors();
	const scale = useTextScale();
	const show = (index: number) => {
		sheet.finish();
		host?.jumpTo(index);
	};
	return (
		<Sheet title={`Comments · ${comments.length}`} done={{ onPress: () => sheet.finish() }}>
			<FlatList
				data={comments}
				keyExtractor={(comment: DocumentComment) => comment.id}
				contentContainerStyle={{ padding: 16, gap: 20 }}
				ListEmptyComponent={
					<View style={{ gap: 4, paddingTop: 24, alignItems: "center" }}>
						<Text
							allowFontScaling={allowFontScaling}
							style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale }}
						>
							No comments yet
						</Text>
						<Text
							allowFontScaling={allowFontScaling}
							style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale }}
						>
							Touch and hold a paragraph to comment on it.
						</Text>
					</View>
				}
				ListFooterComponent={
					host?.canReview && comments.length > 0 ? (
						<Pressable
							accessibilityRole="button"
							accessibilityLabel="Send review"
							onPress={() => navigation.navigate("ReviewSheet", route.params)}
							style={{ minHeight: 44, alignItems: "center", justifyContent: "center" }}
						>
							<Text
								allowFontScaling={allowFontScaling}
								style={{ color: palette.accentInk, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600" }}
							>
								Send review
							</Text>
						</Pressable>
					) : null
				}
				renderItem={({ item: comment }: { item: DocumentComment }) => {
					const anchored = host?.anchor(comment) ?? null;
					return (
						<View style={{ gap: 8 }}>
							<QuoteBlock words={comment.quote} limit={140} size={14} lineHeight={19} />
							<Text
								allowFontScaling={allowFontScaling}
								style={{ color: palette.inkHi, fontSize: 15 * scale, lineHeight: 20 * scale }}
							>
								{comment.text}
							</Text>
							<View style={{ flexDirection: "row", gap: 16 }}>
								{anchored !== null ? <TextButton label="Show" onPress={() => show(anchored)} /> : null}
								<TextButton label="Delete" onPress={() => memory.removeComment(key, comment.id)} />
							</View>
						</View>
					);
				}}
			/>
		</Sheet>
	);
}

function TextButton({ label, onPress }: { label: string; onPress(): void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={onPress}
			hitSlop={8}
			style={{ minHeight: 44, justifyContent: "center" }}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale }}
			>
				{label}
			</Text>
		</Pressable>
	);
}
