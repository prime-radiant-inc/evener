import { SymbolView } from "expo-symbols";
import { Pressable, Text, useWindowDimensions, View } from "react-native";
import { ChipStrip } from "../design/ChipStrip";
import { scaledType, uiType } from "../design/tokens";
import { useHeaderTextScale } from "../headerText";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type Band, type LiveSummary, LIVE_BAND_ORDER, plural, summaryText } from "./attention";
import { FreshDot } from "../reader/FreshDot";
import { FoldChevron } from "./BoardRow";
import { PulseMeter } from "./PulseMeter";
import { haptic } from "../haptics";
import { Button } from "../sheet/Grouped";

/** The hub button (spec 7.1): the hub's name and a chevron as one control,
 * opening the Hub sheet (spec 12). A native bar item given both a label and an
 * icon draws only the icon, so this is a custom header view. The Hub opens
 * while the hub is out of reach too: it keeps its last data and says why its
 * controls wait. Its text follows Dynamic Type up to xxxLarge only, since the
 * bar's height is fixed, so a long press shows the whole name in the Large
 * Content Viewer, as Apple asks of text that stops growing. */
export function HubButton({ hubName, onOpen }: { hubName: string; onOpen: () => void }) {
	const { palette } = useColors();
	const scale = useHeaderTextScale();
	const { width } = useWindowDimensions();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={hubName}
			accessibilityHint="Opens the Hub"
			accessibilityShowsLargeContentViewer
			accessibilityLargeContentTitle={hubName}
			onPress={onOpen}
			style={{
				// A custom header view sizes itself, so a long hub name needs a
				// cap to truncate against instead of growing into Search.
				maxWidth: Math.round(width * 0.6),
				minHeight: 44,
				paddingHorizontal: 12,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 6,
			}}
		>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				style={{ flexShrink: 1, fontSize: 17 * scale, color: palette.inkHi }}
			>
				{hubName}
			</Text>
			<SymbolView name="chevron.down" size={13 * scale} tintColor={palette.inkHi} />
		</Pressable>
	);
}

export interface ChipProps {
	key: string;
	name: string;
	count: number;
	badge?: number;
	pinned?: boolean;
	label: string;
	onPress: () => void;
}

/** The section chips, fixed under the header (spec 7.1), in the shared chip
 * strip that owns the row's fill and its overflow fade. */
export function Chips({ chips, onGlass }: { chips: ChipProps[]; onGlass: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<ChipStrip testID="chips" onGlass={onGlass}>
			{chips.map((chip) => (
				<Pressable
					key={chip.key}
					testID="chip"
					accessibilityRole="button"
					accessibilityLabel={chip.label}
					onPress={() => {
						haptic("selection");
						chip.onPress();
					}}
					// The chip draws 32pt tall; hit slop into the row's 8pt padding makes a 44pt target.
					hitSlop={{ top: 6, bottom: 6 }}
					style={({ pressed }) => ({
						minHeight: 32,
						paddingHorizontal: 12,
						borderRadius: 16,
						flexDirection: "row",
						alignItems: "center",
						columnGap: 6,
						backgroundColor: pressed ? palette.pressed : palette.surface,
						borderWidth: 0.5,
						borderColor: palette.edge,
					})}
				>
					{chip.pinned ? <SymbolView name="pin.fill" size={12 * scale} tintColor={palette.inkLow} /> : null}
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 14 * scale, fontWeight: "600", color: palette.inkHi }}
					>
						{chip.name}
					</Text>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 14 * scale, fontWeight: "500", color: palette.inkLow, fontVariant: ["tabular-nums"] }}
					>
						{String(chip.count)}
					</Text>
					{chip.badge ? (
						<View
							style={{
								minWidth: 18 * scale,
								height: 18 * scale,
								paddingHorizontal: 5,
								borderRadius: 9 * scale,
								alignItems: "center",
								justifyContent: "center",
								backgroundColor: palette.attention,
							}}
						>
							<Text
								allowFontScaling={allowFontScaling}
								style={{
									fontSize: 11 * scale,
									fontWeight: "700",
									fontVariant: ["tabular-nums"],
									// Dark ink on amber in both themes: the light theme's ink,
									// the dark theme's page.
									color: palette.scheme === "dark" ? palette.page : palette.inkHi,
								}}
							>
								{String(chip.badge)}
							</Text>
						</View>
					) : null}
				</Pressable>
			))}
		</ChipStrip>
	);
}

/** The Live summary line (spec 7.1): each count jumps to its band. */
export function SummaryLine({
	summary,
	connected,
	perMinute,
	onJump,
}: {
	summary: LiveSummary;
	connected: boolean;
	/** The fleet meter's per-minute counts; absent, it shows its still fallback. */
	perMinute?: readonly number[];
	onJump: (band: Band) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const entries = LIVE_BAND_ORDER.filter((band) => summary[band] > 0);
	return (
		<View
			testID="live-summary"
			style={{
				flexDirection: "row",
				flexWrap: "wrap",
				alignItems: "center",
				// Wrapped rows sit apart by both counts' slop, so a tap near the
				// wrap lands on the count it's over.
				rowGap: 14,
				paddingTop: 12,
				paddingHorizontal: 16,
			}}
		>
			{entries.map((band, index) => (
				// Each count carries the separator after it, so the line wraps
				// after a dot and no wrapped line starts with one.
				<View key={band} style={{ flexDirection: "row", alignItems: "center" }}>
					<Pressable
						accessibilityRole="button"
						onPress={() => {
							haptic("selection");
							onJump(band);
						}}
						// Each count draws 30pt tall; the slop makes a 44pt target.
						hitSlop={{ top: 7, bottom: 7 }}
						style={({ pressed }) => ({
							minHeight: 30,
							flexDirection: "row",
							alignItems: "center",
							columnGap: 4,
							opacity: pressed ? 0.6 : 1,
						})}
					>
						{band === "working" ? <PulseMeter tone={connected ? "alive" : "gray"} perMinute={perMinute} /> : null}
						<Text
							allowFontScaling={allowFontScaling}
							style={{
								...scaledType(uiType.statusLine, scale),
								fontWeight: band === "needsYou" ? "600" : "400",
								color: band === "needsYou" ? palette.attentionInk : palette.inkMid,
							}}
						>
							{summaryText(band, summary[band])}
						</Text>
					</Pressable>
					{index < entries.length - 1 ? (
						<Text allowFontScaling={allowFontScaling} style={{ fontSize: 14 * scale, color: palette.inkLow }}>
							{" · "}
						</Text>
					) : null}
				</View>
			))}
		</View>
	);
}

/** Idle's header, folded by default; its state persists per device. Folded,
 * it shows the blue dot while any session inside has updates you haven't
 * opened: no count, no words. */
export function IdleFold({
	count,
	folded,
	unseen,
	onToggle,
}: {
	count: number;
	folded: boolean;
	unseen: boolean;
	onToggle: () => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const dot = folded && unseen;
	return (
		<Pressable
			testID="idle-fold"
			accessibilityRole="button"
			accessibilityLabel={`Idle, ${plural(count, "session")}${dot ? ", unread sessions inside" : ""}`}
			accessibilityState={{ expanded: !folded }}
			onPress={onToggle}
			style={({ pressed }) => ({
				minHeight: 48,
				paddingHorizontal: 16,
				flexDirection: "row",
				alignItems: "center",
				justifyContent: "space-between",
				backgroundColor: pressed ? palette.pressed : palette.page,
			})}
		>
			<Text
				testID="band-header"
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 15 * scale, color: palette.inkMid }}
			>
				{`Idle · ${count}`}
			</Text>
			<View style={{ flexDirection: "row", alignItems: "center", columnGap: 8 }}>
				{dot ? <FreshDot /> : null}
				<FoldChevron folded={folded} />
			</View>
		</Pressable>
	);
}

/** The very first load: three still placeholder rows. */
export function Skeleton() {
	const { palette } = useColors();
	return (
		<View style={{ paddingTop: 12, paddingHorizontal: 16, rowGap: 8 }} accessibilityLabel="Loading sessions">
			{["first", "second", "third"].map((key) => (
				<View
					key={key}
					testID="skeleton-row"
					style={{ height: 64, borderRadius: 10, backgroundColor: palette.inset }}
				/>
			))}
		</View>
	);
}

export function FirstReadFailed() {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View testID="empty-state:firstReadFailed" style={{ paddingHorizontal: 16, paddingVertical: 32 }}>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkMid, textAlign: "center" }}
			>
				Couldn't load this hub's sessions. Trying again shortly.
			</Text>
		</View>
	);
}

export function EmptyBoard({ disabled, onNewSession }: { disabled: boolean; onNewSession: () => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			testID="empty-state:noSessions"
			style={{ paddingHorizontal: 16, paddingVertical: 32, alignItems: "center", rowGap: 16 }}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkMid, textAlign: "center" }}
			>
				Nothing's running. Start a session to put an agent to work.
			</Text>
			<Button primary compact label="New session" disabled={disabled} onPress={onNewSession} />
		</View>
	);
}
