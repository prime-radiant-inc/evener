import { memo, useMemo } from "react";
import { type AccessibilityActionEvent, type AccessibilityActionInfo, Alert, Linking } from "react-native";
import { EnrichedMarkdownText } from "react-native-enriched-markdown";
import { copyText } from "./clipboard";
import { useReadingType } from "./display/displayContext";
import { externalMarkdownLink } from "./markdownLinks";
import { splitNativeSegments } from "./markdownSegments";
import { type MarkdownRoles, markdownStyle } from "./markdownStyle";
import { MermaidDiagram } from "./MermaidDiagram";
import { useColors } from "./ui";

// Agent prose in the phone's reading font (the serif unless Display says
// Sans), headings in the system font.
const TRANSCRIPT_HEADINGS: MarkdownRoles["headings"] = [
	{ fontSize: 20, lineHeight: 26, fontWeight: "600" },
	{ fontSize: 17, lineHeight: 24, fontWeight: "600" },
	{ fontSize: 15, lineHeight: 21, fontWeight: "600" },
];

/** A link's destination, with Open in browser when it's a web address. */
export function showLink(target: string) {
	const url = externalMarkdownLink(target);
	Alert.alert(
		url ? "Link" : "Link destination",
		url ? target : `This app cannot open this destination yet.\n\n${target}`,
		[
			...(url
				? [
						{
							text: "Open in browser",
							onPress: () => {
								void openLink(url);
							},
						},
					]
				: []),
			{
				text: "Copy destination",
				onPress: () => {
					void copyText(target);
				},
			},
			{ text: "Cancel", style: "cancel" },
		],
	);
}
/** Opens a web address; anything else shows its destination. */
export async function openLink(target: string) {
	const url = externalMarkdownLink(target);
	if (!url) {
		showLink(target);
		return;
	}
	try {
		await Linking.openURL(url);
	} catch {
		Alert.alert("Could not open link", url, [
			{
				text: "Copy destination",
				onPress: () => {
					void copyText(target);
				},
			},
			{ text: "Cancel", style: "cancel" },
		]);
	}
}

// `selectable` false hands touch and hold to the caller's own menu (the
// transcript's agent message), so the text's native selection menu, and the
// "Copy response" item added to it, go with it.
export const MarkdownResponse = memo(function MarkdownResponse({
	markdown,
	selectable = true,
	accessibilityActions,
	onAccessibilityAction,
}: {
	markdown: string;
	selectable?: boolean;
	accessibilityActions?: AccessibilityActionInfo[];
	onAccessibilityAction?: (event: AccessibilityActionEvent) => void;
}) {
	const colors = useColors();
	// useColors returns a new object each render, but every color in it comes
	// from the palette, one constant per color scheme.
	const reading = useReadingType();
	// biome-ignore lint/correctness/useExhaustiveDependencies: keyed on the palette, as above
	const style = useMemo(
		() => markdownStyle(colors, { body: reading.agentProse, headings: TRANSCRIPT_HEADINGS }),
		[colors.palette, reading],
	);
	const segments = useMemo(() => splitNativeSegments(markdown), [markdown]);
	return (
		<>
			{segments.map((segment, index) =>
				segment.kind === "mermaid" ? (
					<MermaidDiagram
						key={index}
						source={segment.source}
						accessibilityActions={accessibilityActions}
						onAccessibilityAction={onAccessibilityAction}
					/>
				) : (
					<EnrichedMarkdownText
						key={index}
						markdown={segment.source}
						markdownStyle={style}
						flavor="github"
						selectable={selectable}
						allowFontScaling
						enableTaskListItemToggle={false}
						streamingAnimation={false}
						spoilerOverlay="solid"
						onLinkPress={({ url }) => {
							void openLink(url);
						}}
						onLinkLongPress={({ url }) => showLink(url)}
						contextMenuItems={
							selectable
								? [
										{
											text: "Copy response",
											onPress: () => {
												void copyText(markdown);
											},
										},
									]
								: undefined
						}
						accessibilityActions={accessibilityActions}
						onAccessibilityAction={onAccessibilityAction}
					/>
				),
			)}
		</>
	);
});
