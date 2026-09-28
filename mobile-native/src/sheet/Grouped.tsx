// The grouped list the Hub and New session sheets are built from (spec 12 and
// 16): uppercase section labels, inset groups of rows on the surface color over
// the canvas, and ink-low footers. A row carries a bare SF Symbol in ink-mid
// (never a colored tile), a label with an optional second line, a trailing
// value, and a chevron when it opens a page.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { Children, Fragment, isValidElement, type ReactNode } from "react";
import { Platform, Pressable, ScrollView, Switch, Text, View } from "react-native";
import { fonts } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";

/** The scrolling page a grouped sheet page sits in. */
export function GroupedPage({ children }: { children: ReactNode }) {
	const { palette } = useColors();
	return (
		<ScrollView
			style={{ flex: 1, backgroundColor: palette.canvas }}
			contentContainerStyle={{ paddingBottom: 32 }}
			keyboardShouldPersistTaps="handled"
			automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
		>
			{children}
		</ScrollView>
	);
}

/** A section label: SF Pro semibold 12, uppercase, +0.06em (spec 16.2). A
 * machine label (a marketplace, a provider profile) is Menlo as typed and never
 * uppercased (spec 11). */
export function GroupLabel({ children, machine = false }: { children: string; machine?: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			accessibilityRole="header"
			allowFontScaling={allowFontScaling}
			style={[
				{
					color: palette.inkMid,
					fontSize: 12 * scale,
					lineHeight: 16 * scale,
					paddingHorizontal: 32,
					paddingTop: 20,
					paddingBottom: 6,
				},
				machine
					? { fontFamily: fonts.mono }
					: ({ fontWeight: "600", letterSpacing: 0.72, textTransform: "uppercase" } as const),
			]}
		>
			{children}
		</Text>
	);
}

/** An inset group of rows with hairlines between them, not around them. */
export function Group({ children }: { children: ReactNode }) {
	const { palette } = useColors();
	const rows = Children.toArray(children);
	return (
		<View style={{ marginHorizontal: 16, borderRadius: 12, overflow: "hidden", backgroundColor: palette.surface }}>
			{rows.map((row, index) => (
				<Fragment key={isValidElement(row) && row.key !== null ? row.key : `row-${index}`}>
					{index > 0 ? (
						<View testID="hairline" style={{ height: 0.5, marginLeft: 16, backgroundColor: palette.edge }} />
					) : null}
					{row}
				</Fragment>
			))}
		</View>
	);
}

/** A row's leading slot: a bare symbol, or the empty space an unchecked
 * picker row keeps so its label lines up with the checked one. */
function Glyph({ name, color }: { name: SFSymbol | undefined; color: string }) {
	return (
		<View style={{ width: 22, alignItems: "center" }}>
			{name ? <SymbolView name={name} tintColor={color} size={17} /> : null}
		</View>
	);
}

export interface RowProps {
	label: string;
	/** A second line in ink-low. */
	sub?: string;
	/** The second line is a path or an id: Menlo. */
	machineSub?: boolean;
	icon?: SFSymbol;
	/** The trailing value: text in ink-mid, or a node such as a tag. */
	value?: ReactNode;
	/** The row opens a page. */
	chevron?: boolean;
	/** "accent" for an action ("Connect", "Browse folders on …"), "danger"
	 * for a destructive one ("Remove"). */
	tone?: "normal" | "accent" | "danger";
	/** A picker's row: true draws the check, false leaves the space. */
	checked?: boolean;
	disabled?: boolean;
	onPress?: () => void;
	/** VoiceOver's reading when the visible text isn't enough. */
	accessibilityLabel?: string;
}

export function Row({
	label,
	sub,
	machineSub = false,
	icon,
	value,
	chevron = false,
	tone = "normal",
	checked,
	disabled = false,
	onPress,
	accessibilityLabel,
}: RowProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	const labelColor = tone === "accent" ? palette.accentInk : tone === "danger" ? palette.dangerInk : palette.inkHi;
	const plainValue = typeof value === "string" || typeof value === "number" ? String(value) : undefined;
	const reading = accessibilityLabel ?? [label, sub, plainValue].filter(Boolean).join(", ");
	const body = (
		<>
			{checked !== undefined ? (
				<Glyph name={checked ? "checkmark" : undefined} color={palette.accentInk} />
			) : icon ? (
				<Glyph name={icon} color={palette.inkMid} />
			) : null}
			<View style={{ flex: 1, gap: 2 }}>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: labelColor, fontSize: 17 * scale, lineHeight: 22 * scale }}
				>
					{label}
				</Text>
				{sub ? (
					<Text
						allowFontScaling={allowFontScaling}
						style={[
							{ color: palette.inkLow, fontSize: 13 * scale, lineHeight: 18 * scale },
							machineSub ? { fontFamily: fonts.mono } : null,
						]}
					>
						{sub}
					</Text>
				) : null}
			</View>
			{plainValue !== undefined ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.inkMid, fontSize: 17 * scale, fontVariant: ["tabular-nums"] }}
				>
					{plainValue}
				</Text>
			) : (
				(value ?? null)
			)}
			{chevron ? <SymbolView name="chevron.right" tintColor={palette.inkLow} size={13} /> : null}
		</>
	);
	const style = {
		flexDirection: "row",
		alignItems: "center",
		gap: 12,
		minHeight: 44,
		paddingHorizontal: 16,
		paddingVertical: 11,
		opacity: disabled ? 0.4 : 1,
	} as const;
	if (!onPress)
		return (
			<View accessible accessibilityLabel={reading} style={style}>
				{body}
			</View>
		);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={reading}
			accessibilityState={checked === undefined ? { disabled } : { disabled, selected: checked }}
			disabled={disabled}
			onPress={onPress}
			style={({ pressed }) => [style, pressed ? { backgroundColor: palette.pressed } : null]}
		>
			{body}
		</Pressable>
	);
}

/** A row whose trailing control is a switch in the accent color (spec 16.1:
 * switches are accent, never the working green). The switch is the accessible
 * element, so VoiceOver can flip it. */
export function SwitchRow({
	label,
	sub,
	icon,
	value,
	onChange,
	disabled = false,
}: {
	label: string;
	sub?: string;
	icon?: SFSymbol;
	value: boolean;
	onChange(value: boolean): void;
	disabled?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{
				flexDirection: "row",
				alignItems: "center",
				gap: 12,
				minHeight: 44,
				paddingHorizontal: 16,
				paddingVertical: 8,
				opacity: disabled ? 0.4 : 1,
			}}
		>
			{icon ? <Glyph name={icon} color={palette.inkMid} /> : null}
			<View style={{ flex: 1, gap: 2 }} accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale }}
				>
					{label}
				</Text>
				{sub ? (
					<Text
						allowFontScaling={allowFontScaling}
						style={{ color: palette.inkLow, fontSize: 13 * scale, lineHeight: 18 * scale }}
					>
						{sub}
					</Text>
				) : null}
			</View>
			<Switch
				accessibilityLabel={label}
				accessibilityHint={sub}
				value={value}
				disabled={disabled}
				onValueChange={onChange}
				trackColor={{ false: palette.edgeStrong, true: palette.accent }}
				// iOS draws the off track from this, not from trackColor.false.
				ios_backgroundColor={palette.edgeStrong}
			/>
		</View>
	);
}

/** A group's footer: ink-low, or the attention or danger ink when it reports
 * something a person must act on. */
export function GroupFooter({
	children,
	tone = "normal",
}: {
	children: string;
	tone?: "normal" | "attention" | "danger";
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const color = tone === "attention" ? palette.attentionInk : tone === "danger" ? palette.dangerInk : palette.inkLow;
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				color,
				fontSize: 13 * scale,
				lineHeight: 18 * scale,
				paddingHorizontal: 32,
				paddingTop: 6,
				paddingBottom: 8,
			}}
		>
			{children}
		</Text>
	);
}

/** A segmented control (spec 16.1: the chosen segment is accent-bg with accent
 * ink, never an ink fill). A tap on the chosen segment does nothing. */
export function Segmented<T extends string>({
	label,
	options,
	value,
	onChange,
	disabled = false,
}: {
	label: string;
	options: readonly { value: T; label: string }[];
	value: T | null;
	onChange(value: T): void;
	disabled?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			accessibilityRole="radiogroup"
			accessibilityLabel={label}
			style={{
				flexDirection: "row",
				gap: 2,
				marginHorizontal: 16,
				padding: 2,
				borderRadius: 22,
				backgroundColor: palette.inset,
				opacity: disabled ? 0.4 : 1,
			}}
		>
			{options.map((option) => {
				const chosen = option.value === value;
				return (
					<Pressable
						key={option.value}
						accessibilityRole="radio"
						accessibilityLabel={option.label}
						accessibilityState={{ checked: chosen, disabled }}
						disabled={disabled}
						onPress={() => {
							if (!chosen) onChange(option.value);
						}}
						style={{
							flex: 1,
							minHeight: 40,
							alignItems: "center",
							justifyContent: "center",
							borderRadius: 20,
							backgroundColor: chosen ? palette.accentBg : "transparent",
						}}
					>
						<Text
							allowFontScaling={allowFontScaling}
							numberOfLines={1}
							style={{
								color: chosen ? palette.accentInk : palette.inkHi,
								fontSize: 15 * scale,
								fontWeight: chosen ? "600" : "400",
							}}
						>
							{option.label}
						</Text>
					</Pressable>
				);
			})}
		</View>
	);
}

const TAG_TONES = {
	amber: ["attentionInk", "attentionBg"],
	gray: ["inkMid", "inset"],
	blue: ["accentInk", "accentBg"],
	red: ["dangerInk", "dangerBg"],
} as const;

/** A small capsule beside a value: an amber "Offline", a gray "Hub runs
 * 0.9.412" (spec 16.1: version drift is a gray tag). */
export function Tag({ text, tone }: { text: string; tone: keyof typeof TAG_TONES }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const [ink, fill] = TAG_TONES[tone];
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				color: palette[ink],
				backgroundColor: palette[fill],
				fontSize: 11 * scale,
				lineHeight: 13 * scale,
				fontWeight: "600",
				paddingHorizontal: 5,
				paddingVertical: 2,
				borderRadius: 4,
				overflow: "hidden",
			}}
		>
			{text}
		</Text>
	);
}
