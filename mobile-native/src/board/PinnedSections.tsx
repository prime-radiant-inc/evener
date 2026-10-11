import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { type ReactNode, useCallback, useReducer } from "react";
import { type LayoutChangeEvent, Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { sectionLabel } from "./attention";
import { bandHeaderText, FoldChevron } from "./BoardRow";
import { foldedSections } from "./nativeBoardMemory";

const EMPTY_HINT = "Touch and hold a session and choose Pin to category.";

/** The Board's folds, per device and hub. FoldedSections has no
 * subscribers, so setting a fold redraws the component this hook is in;
 * `isFolded` changes identity with each fold set here, so a memo can list it. */
export function useBoardFolds(hubId: string) {
	const sections = foldedSections(hubId);
	const [revision, redraw] = useReducer((count: number) => count + 1, 0);
	// revision stands for a fold set here, which sections can't signal.
	const isFolded = useCallback(
		(fold: string, byDefault: boolean) => sections.isFolded(fold, byDefault),
		[sections, revision],
	);
	return {
		isFolded,
		setFolded: (fold: string, folded: boolean) => {
			sections.setFolded(fold, folded);
			redraw();
		},
	};
}

/** Each pinned category's fold, per device and hub, under `pin:<id>`: a
 * category starts unfolded. */
export function useCategoryFolds(hubId: string) {
	const folds = useBoardFolds(hubId);
	const isFolded = useCallback((id: string) => folds.isFolded(`pin:${id}`, false), [folds.isFolded]);
	return {
		isFolded,
		setFolded: (id: string, folded: boolean) => folds.setFolded(`pin:${id}`, folded),
	};
}

export interface PinnedSectionProps {
	section: NavigationPinSectionDescriptor;
	folded: boolean;
	onToggle: () => void;
	/** Opens Rename and Delete, or null while no change can go out, which
	 * hides ⋯. */
	onMenu: (() => void) | null;
	/** A change to this category is on its way. */
	changing: boolean;
	onLayout?: (event: LayoutChangeEvent) => void;
	/** What the category holds under its header (boardItems.ts pinnedItems). */
	children?: ReactNode;
}

/** What an empty category says, once its page has loaded. */
export function PinnedEmptyHint() {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
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
}

/** One pinned category on the Board (spec 7.1): a header like the Board's
 * band headers, then what it holds. */
export function PinnedSection({ section, folded, onToggle, onMenu, changing, onLayout, children }: PinnedSectionProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const headerText = bandHeaderText(palette, scale);
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
						allowFontScaling={allowFontScaling}
						numberOfLines={1}
						style={{ ...headerText, flexShrink: 1, textTransform: "uppercase" }}
					>
						{section.name}
					</Text>
					<Text allowFontScaling={allowFontScaling} style={{ ...headerText, fontVariant: ["tabular-nums"] }}>
						{` · ${section.count}`}
					</Text>
					<View style={{ flex: 1, alignItems: "flex-end" }}>
						<FoldChevron folded={folded} />
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
			{children}
		</View>
	);
}
