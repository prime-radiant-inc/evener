import type { NavigationPinSectionDescriptor, NavigationSessionSummary } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { useReducer } from "react";
import { type LayoutChangeEvent, Platform, Pressable, Text, View } from "react-native";
import { useColors, useTextScale } from "../ui";
import { type ClassifiedRow, sectionLabel } from "./attention";
import { BoardRows, type RowContext } from "./BoardRow";
import { foldedSections } from "./nativeBoardMemory";

const EMPTY_HINT = "Touch and hold a session and choose Pin to category.";

/** Each pinned category's fold, per device and hub, under `pin:<id>`: a
 * category starts unfolded. */
export function useCategoryFolds(hubId: string) {
	const sections = foldedSections(hubId);
	const [, redraw] = useReducer((revision: number) => revision + 1, 0);
	return {
		isFolded: (id: string) => sections.isFolded(`pin:${id}`, false),
		setFolded: (id: string, folded: boolean) => {
			sections.setFolded(`pin:${id}`, folded);
			redraw();
		},
	};
}

export interface PinnedSectionProps {
	section: NavigationPinSectionDescriptor;
	/** The category's sessions; absent or unloaded until its first read lands. */
	page: { loaded: boolean; rows: readonly NavigationSessionSummary[] } | undefined;
	classify: (row: NavigationSessionSummary) => ClassifiedRow;
	context: RowContext;
	folded: boolean;
	onToggle: () => void;
	/** Opens Rename and Delete, or null while no change can go out, which
	 * hides ⋯. */
	onMenu: (() => void) | null;
	/** A change to this category is on its way. */
	changing: boolean;
	onLayout?: (event: LayoutChangeEvent) => void;
}

/** One pinned category on the Board (spec 7.1): a header like the Board's
 * band headers, then its sessions as quiet, still rows. A pinned category is
 * a place; a live session's full row is already in Live. */
export function PinnedSection({
	section,
	page,
	classify,
	context,
	folded,
	onToggle,
	onMenu,
	changing,
	onLayout,
}: PinnedSectionProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const headerText = {
		fontSize: 13 * scale,
		fontWeight: "600" as const,
		letterSpacing: 0.4,
		color: palette.inkMid,
	};
	let body = null;
	if (!folded && page?.loaded)
		body = page.rows.length ? (
			<BoardRows items={page.rows.map(classify)} variant="quiet" moving={false} context={context} />
		) : (
			<Text
				allowFontScaling={Platform.OS !== "ios"}
				style={{
					marginHorizontal: 16,
					paddingBottom: 12,
					fontSize: 15 * scale,
					lineHeight: 20 * scale,
					color: palette.inkMid,
				}}
			>
				{EMPTY_HINT}
			</Text>
		);
	return (
		<View testID="pin-section" onLayout={onLayout} style={{ paddingTop: 10, opacity: changing ? 0.5 : 1 }}>
			<View style={{ flexDirection: "row", alignItems: "center" }}>
				<Pressable
					testID="pin-header"
					accessibilityRole="button"
					accessibilityLabel={sectionLabel(section.name, section.count, "session")}
					accessibilityState={{ expanded: !folded }}
					onPress={onToggle}
					style={({ pressed }) => ({
						flex: 1,
						minHeight: 44,
						paddingLeft: 16,
						paddingRight: onMenu ? 8 : 16,
						flexDirection: "row",
						alignItems: "center",
						backgroundColor: pressed ? palette.pressed : palette.page,
					})}
				>
					{/* No gap between the name and its count, so they read as one
					    label; the pin keeps its distance with its own margin. */}
					<SymbolView name="pin.fill" size={12 * scale} tintColor={palette.inkLow} style={{ marginRight: 6 }} />
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						numberOfLines={1}
						style={{ ...headerText, flexShrink: 1, textTransform: "uppercase" }}
					>
						{section.name}
					</Text>
					<Text allowFontScaling={Platform.OS !== "ios"} style={{ ...headerText, fontVariant: ["tabular-nums"] }}>
						{` · ${section.count}`}
					</Text>
					<View style={{ flex: 1, alignItems: "flex-end" }}>
						<View style={{ transform: [{ rotate: folded ? "0deg" : "90deg" }] }}>
							<SymbolView name="chevron.right" size={13 * scale} tintColor={palette.inkLow} />
						</View>
					</View>
				</Pressable>
				{onMenu ? (
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={`${section.name}, category menu`}
						onPress={onMenu}
						style={({ pressed }) => ({
							minWidth: 44,
							minHeight: 44,
							marginRight: 8,
							alignItems: "center",
							justifyContent: "center",
							opacity: pressed ? 0.6 : 1,
						})}
					>
						<SymbolView name="ellipsis.circle" size={20 * scale} tintColor={palette.inkLow} />
					</Pressable>
				) : null}
			</View>
			{body}
		</View>
	);
}
