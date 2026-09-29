// The grouped list the Hub and New session sheets are built from (spec 12 and
// 16): uppercase section labels, inset groups of rows on the surface color over
// the canvas, and ink-mid footers. A row carries a bare SF Symbol in ink-mid
// (never a colored tile), a label with an optional second line, a trailing
// value, and a chevron when it opens a page.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { Children, Fragment, isValidElement, type ReactNode } from "react";
import { Platform, Pressable, ScrollView, Switch, Text, TextInput, View } from "react-native";
import { fonts, scaledType, space, uiType } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";

/** Above a section label. */
const SECTION_TOP = 20;
/** A row's leading glyph slot. */
const GLYPH = 22;

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
 * uppercased (spec 11). A group takes its label as Group's `label`; this
 * heads anything else, such as a segmented control or a footer. */
export function GroupLabel({ children, machine = false }: { children: string; machine?: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const { fontSize, lineHeight, ...sectionCase } = scaledType(uiType.sectionLabel, scale);
	return (
		<Text
			accessibilityRole="header"
			allowFontScaling={allowFontScaling}
			style={[
				{
					color: palette.inkMid,
					fontSize,
					lineHeight,
					paddingHorizontal: space.labelInset,
					paddingTop: SECTION_TOP,
					paddingBottom: 6,
				},
				machine ? { fontFamily: fonts.mono } : sectionCase,
			]}
		>
			{children}
		</Text>
	);
}

/** Where a row's text starts when the row carries a symbol. */
const ICON_ROW_TEXT = space.rowInset + GLYPH + space.rowGap;

/** Reads a child's `icon` prop, so only a Row or SwitchRow placed directly in
 * the group counts: a row wrapped in another component, or a custom child,
 * gets the plain 16pt hairline inset even if it draws a symbol. */
function carriesSymbol(row: ReactNode): boolean {
	return isValidElement<{ icon?: unknown }>(row) && Boolean(row.props.icon);
}

/** An inset group of rows with hairlines between them, not around them. A
 * group with a section label sits right under it; one without keeps the
 * prototype's 16pt from whatever comes before it (`.group + .group`), so two
 * groups in a row never touch. A hairline starts under the text when the rows
 * on both sides of it carry a symbol, as iOS Settings draws it. */
export function Group({
	children,
	label,
	machineLabel = false,
}: {
	children: ReactNode;
	label?: string;
	/** The label is a marketplace or similar machine name (see GroupLabel). */
	machineLabel?: boolean;
}) {
	const { palette } = useColors();
	const rows = Children.toArray(children);
	const group = (
		<View
			style={{
				marginHorizontal: space.margin,
				marginTop: label ? 0 : space.groupGap,
				borderRadius: 12,
				overflow: "hidden",
				backgroundColor: palette.surface,
			}}
		>
			{rows.map((row, index) => (
				<Fragment key={isValidElement(row) && row.key !== null ? row.key : `row-${index}`}>
					{index > 0 ? (
						<View
							testID="hairline"
							style={{
								height: 0.5,
								marginLeft: carriesSymbol(rows[index - 1]) && carriesSymbol(row) ? ICON_ROW_TEXT : space.rowInset,
								backgroundColor: palette.edge,
							}}
						/>
					) : null}
					{row}
				</Fragment>
			))}
		</View>
	);
	if (!label) return group;
	return (
		<>
			<GroupLabel machine={machineLabel}>{label}</GroupLabel>
			{group}
		</>
	);
}

/** The space above a page's first block when that block isn't a group, such
 * as the Plugins page's segmented control: a group keeps its own space. */
export function GroupGap() {
	return <View style={{ height: space.groupGap }} />;
}

/** A row's leading slot: a bare symbol, or the empty space an unchecked
 * picker row keeps so its label lines up with the checked one. */
function Glyph({ name, color }: { name: SFSymbol | undefined; color: string }) {
	return (
		<View style={{ width: GLYPH, alignItems: "center" }}>
			{name ? <SymbolView name={name} tintColor={color} size={17} /> : null}
		</View>
	);
}

export interface RowProps {
	label: string;
	/** The label is a model id or similar machine text: Menlo. */
	machineLabel?: boolean;
	/** A second line in ink-mid. */
	sub?: string;
	/** The second line is a path or an id: Menlo. */
	machineSub?: boolean;
	/** "danger" for a second line that reports a problem ("1 need attention"). */
	subTone?: "normal" | "danger";
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
	machineLabel = false,
	sub,
	machineSub = false,
	subTone = "normal",
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
			{/* The label and value share one wrapping line. The label is sized by
			    its own text and grows into the free space, so a value that
			    doesn't fit beside it moves under it: the label's words never
			    break apart to make room ("paradise-/park"). */}
			<View
				testID="row-line"
				style={{
					flex: 1,
					flexDirection: "row",
					flexWrap: "wrap",
					alignItems: "center",
					columnGap: space.rowGap,
					rowGap: 4,
				}}
			>
				<View testID="row-label" style={{ flexGrow: 1, flexShrink: 1, gap: 2 }}>
					<Text
						allowFontScaling={allowFontScaling}
						style={{
							color: labelColor,
							...scaledType(uiType.listRow, scale),
							...(machineLabel ? { fontFamily: fonts.mono } : null),
						}}
					>
						{label}
					</Text>
					{sub ? (
						<Text
							allowFontScaling={allowFontScaling}
							style={[
								{
									color: subTone === "danger" ? palette.dangerInk : palette.inkMid,
									...scaledType(uiType.footnote, scale),
								},
								machineSub ? { fontFamily: fonts.mono } : null,
							]}
						>
							{sub}
						</Text>
					) : null}
				</View>
				{value === undefined || value === null ? null : (
					<View testID="row-value" style={{ flexShrink: 1 }}>
						{plainValue !== undefined ? <RowValue text={plainValue} /> : value}
					</View>
				)}
			</View>
			{chevron ? <SymbolView name="chevron.right" tintColor={palette.inkLow} size={13} /> : null}
		</>
	);
	const style = {
		flexDirection: "row",
		alignItems: "center",
		gap: space.rowGap,
		minHeight: 44,
		paddingHorizontal: space.rowInset,
		paddingVertical: space.rowPadding,
		opacity: disabled ? 0.4 : 1,
	} as const;
	const state = checked === undefined ? { disabled } : { disabled, selected: checked };
	if (!onPress)
		return (
			<View accessible accessibilityLabel={reading} accessibilityState={state} style={style}>
				{body}
			</View>
		);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={reading}
			accessibilityState={state}
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
 * element, so VoiceOver can flip it. With `onPress`, the row's text also opens
 * something (an installed plugin's detail) as its own button, and the row
 * stays undimmed while only the switch is disabled. */
export function SwitchRow({
	label,
	sub,
	subTone = "normal",
	icon,
	value,
	onChange,
	disabled = false,
	onPress,
	accessibilityLabel,
	switchLabel,
}: {
	label: string;
	sub?: string;
	/** "danger" for a second line that reports a problem, such as "Broken". */
	subTone?: "normal" | "danger";
	icon?: SFSymbol;
	value: boolean;
	onChange(value: boolean): void;
	disabled?: boolean;
	/** The row's text opens a detail. */
	onPress?: () => void;
	/** VoiceOver's reading of the text `onPress` opens. */
	accessibilityLabel?: string;
	/** VoiceOver's name for the switch, when the label alone doesn't say what
	 * it sets ("superpowers on by default"). */
	switchLabel?: string;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const text = (
		<View style={{ flex: 1, gap: 2 }} accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
			<Text allowFontScaling={allowFontScaling} style={{ color: palette.inkHi, ...scaledType(uiType.listRow, scale) }}>
				{label}
			</Text>
			{sub ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{
						color: subTone === "danger" ? palette.dangerInk : palette.inkMid,
						...scaledType(uiType.footnote, scale),
					}}
				>
					{sub}
				</Text>
			) : null}
		</View>
	);
	return (
		<View
			style={{
				flexDirection: "row",
				alignItems: "center",
				gap: space.rowGap,
				minHeight: 44,
				paddingHorizontal: space.rowInset,
				paddingVertical: 8,
				opacity: disabled && !onPress ? 0.4 : 1,
			}}
		>
			{icon ? <Glyph name={icon} color={palette.inkMid} /> : null}
			{onPress ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={accessibilityLabel ?? [label, sub].filter(Boolean).join(", ")}
					onPress={onPress}
					style={({ pressed }) => ({
						flex: 1,
						alignSelf: "stretch",
						justifyContent: "center",
						opacity: pressed ? 0.6 : 1,
					})}
				>
					{text}
				</Pressable>
			) : (
				text
			)}
			<Switch
				accessibilityLabel={switchLabel ?? label}
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

/** A row that edits a machine value (an address, a path, a list of paths) in
 * Menlo, as typed: no capitals or corrections. The label is VoiceOver's name
 * for it; the page's section label shows it. */
export function TextFieldRow({
	label,
	value,
	onChangeText,
	multiline = false,
	disabled = false,
}: {
	label: string;
	value: string;
	onChangeText(text: string): void;
	multiline?: boolean;
	disabled?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<TextInput
			accessibilityLabel={label}
			value={value}
			onChangeText={onChangeText}
			multiline={multiline}
			editable={!disabled}
			autoCapitalize="none"
			autoCorrect={false}
			spellCheck={false}
			allowFontScaling={allowFontScaling}
			style={{
				color: palette.inkHi,
				fontFamily: fonts.mono,
				fontSize: uiType.subheadline.fontSize * scale,
				minHeight: multiline ? 88 : 44,
				paddingHorizontal: space.rowInset,
				paddingVertical: space.rowPadding,
				textAlignVertical: multiline ? "top" : "center",
				opacity: disabled ? 0.4 : 1,
			}}
		/>
	);
}

/** A group's footer: ink-mid, or the attention or danger ink when it reports
 * something a person must act on. */
export function GroupFooter({
	children,
	tone = "normal",
	machine = false,
}: {
	children: string;
	tone?: "normal" | "attention" | "danger";
	/** Text the machine wrote, such as an error the hub reported: Menlo. */
	machine?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const color = tone === "attention" ? palette.attentionInk : tone === "danger" ? palette.dangerInk : palette.inkMid;
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={[
				{
					color,
					...scaledType(uiType.footnote, scale),
					paddingHorizontal: space.labelInset,
					paddingTop: 6,
					paddingBottom: 8,
				},
				machine ? { fontFamily: fonts.mono } : null,
			]}
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
				marginHorizontal: space.margin,
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
								fontSize: uiType.subheadline.fontSize * scale,
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

/** A row's trailing value with a tag beside it: a host's version and its gray
 * "Hub runs 0.9.412", or the Hosts count and its amber "1 offline". */
export function RowValue({
	text,
	tag,
	tone = "normal",
}: {
	text?: string;
	tag?: { text: string; tone: keyof typeof TAG_TONES } | null;
	/** "attention" for a value that may need a human, such as Offline. */
	tone?: "normal" | "attention";
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ flexShrink: 1, flexDirection: "row", flexWrap: "wrap", alignItems: "center", gap: 6 }}>
			{text ? (
				<Text
					allowFontScaling={allowFontScaling}
					style={{
						flexShrink: 1,
						color: tone === "attention" ? palette.attentionInk : palette.inkMid,
						fontSize: uiType.listRow.fontSize * scale,
						fontVariant: ["tabular-nums"],
					}}
				>
					{text}
				</Text>
			) : null}
			{tag ? <Tag text={tag.text} tone={tag.tone} /> : null}
		</View>
	);
}

/** A filter field over a list, on the inset fill: a magnifying glass, the
 * field, and a clear button once something is typed. `label` is both its
 * VoiceOver label and its placeholder. */
export function SearchField({
	label,
	query,
	onChange,
}: {
	label: string;
	query: string;
	onChange(query: string): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{
				marginHorizontal: space.margin,
				minHeight: 36,
				flexDirection: "row",
				alignItems: "center",
				gap: 6,
				paddingHorizontal: 10,
				borderRadius: 10,
				backgroundColor: palette.inset,
			}}
		>
			<SymbolView name="magnifyingglass" size={15} tintColor={palette.inkLow} />
			<TextInput
				accessibilityLabel={label}
				placeholder={label}
				placeholderTextColor={palette.inkLow}
				value={query}
				onChangeText={onChange}
				autoCorrect={false}
				allowFontScaling={allowFontScaling}
				style={{ flex: 1, color: palette.inkHi, fontSize: uiType.listRow.fontSize * scale, paddingVertical: 8 }}
			/>
			{query ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Clear filter"
					onPress={() => onChange("")}
					hitSlop={10}
				>
					<SymbolView name="xmark.circle.fill" size={15} tintColor={palette.inkLow} />
				</Pressable>
			) : null}
		</View>
	);
}
