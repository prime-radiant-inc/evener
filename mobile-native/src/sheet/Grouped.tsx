// The grouped list the Hub and New session sheets are built from (spec 12 and
// 16): uppercase section labels, inset groups of rows on the surface color over
// the canvas, and ink-mid footers. A row carries a bare SF Symbol in ink-mid
// (never a colored tile), a label with an optional second line, a trailing
// value, and a chevron when it opens a page.
import { type SFSymbol, SymbolView } from "expo-symbols";
import {
	Children,
	Fragment,
	isValidElement,
	type ReactNode,
	type Ref,
	type RefObject,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import { AccessibilityInfo, Platform, Pressable, ScrollView, Switch, Text, TextInput, View } from "react-native";
import { fonts, scaledType, space, uiType } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";

/** Above a section label. */
const SECTION_TOP = 20;
/** A row's leading glyph slot. */
const GLYPH = 22;

/** The scrolling page a grouped sheet page sits in. `scrollRef` is for a page
 * that brings something into view itself, such as a form's refusal. */
export function GroupedPage({ children, scrollRef }: { children: ReactNode; scrollRef?: Ref<ScrollView> }) {
	const { palette } = useColors();
	return (
		<ScrollView
			ref={scrollRef}
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

/** Between the label and a value that wrapped under it. */
const WRAPPED_VALUE_GAP = 4;

/** Whether a node draws anything: React renders nothing for null,
 * undefined, booleans and the empty string. */
function rendersSomething(node: ReactNode): boolean {
	return node !== undefined && node !== null && typeof node !== "boolean" && node !== "";
}

/** A row's label and its second line: Row draws the label on a wrapping line
 * with its value, SwitchRow stacks the two. One definition keeps the two rows'
 * text in step. */
function RowText({
	label,
	sub,
	subTone = "normal",
	machineLabel = false,
	machineSub = false,
	labelColor,
	children,
}: {
	label: string;
	sub?: string;
	subTone?: "normal" | "danger";
	machineLabel?: boolean;
	machineSub?: boolean;
	/** A row's action or destructive ink overrides the default inkHi. */
	labelColor?: string;
	/** A row's value, drawn on the label's wrapping line. */
	children?: ReactNode;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<>
			<View
				testID="row-line"
				style={{
					flexDirection: "row",
					flexWrap: "wrap",
					alignItems: "center",
					columnGap: space.rowGap,
					rowGap: WRAPPED_VALUE_GAP,
				}}
			>
				<Text
					testID="row-label"
					allowFontScaling={allowFontScaling}
					style={{
						flexGrow: 1,
						flexShrink: 1,
						color: labelColor ?? palette.inkHi,
						...scaledType(uiType.listRow, scale),
						...(machineLabel ? { fontFamily: fonts.mono } : null),
					}}
				>
					{label}
				</Text>
				{children}
			</View>
			{sub ? (
				<Text
					testID="row-sub"
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
		</>
	);
}

/** A grouped row. The label and the value share one wrapping line, with the
 * second line below it. The label is sized by its own text and grows into the
 * free space, so a value that doesn't fit beside it moves under it: the
 * label's words never break apart to make room ("paradise-/park"). */
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
				<RowText
					label={label}
					sub={sub}
					subTone={subTone}
					machineLabel={machineLabel}
					machineSub={machineSub}
					labelColor={labelColor}
				>
					{rendersSomething(value) ? (
						<View testID="row-value" style={{ flexShrink: 1 }}>
							{plainValue !== undefined ? <RowValue text={plainValue} /> : value}
						</View>
					) : null}
				</RowText>
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
	const text = (
		<View style={{ flex: 1, gap: 2 }} accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
			<RowText label={label} sub={sub} subTone={subTone} />
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

/** A row that edits a value in place. By default it's a machine value (an
 * address, a path, a list of paths) in Menlo, as typed: no capitals or
 * corrections. `machine={false}` is for words someone chose, such as a hub's
 * name, in SF Pro at the row size. The label is VoiceOver's name for it; the
 * group's label shows it. */
export function TextFieldRow({
	label,
	value,
	onChangeText,
	multiline = false,
	disabled = false,
	machine = true,
	placeholder,
	secure = false,
	returnKeyType,
	onSubmitEditing,
	ref,
}: {
	label: string;
	value: string;
	onChangeText(text: string): void;
	multiline?: boolean;
	disabled?: boolean;
	machine?: boolean;
	placeholder?: string;
	/** A token or key: the field hides what's typed, and iOS never offers to
	 * fill or save it. */
	secure?: boolean;
	/** "next" to lead on to the form's next field, "done" on its last. */
	returnKeyType?: "next" | "done";
	onSubmitEditing?: () => void;
	ref?: Ref<TextInput>;
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
			placeholder={placeholder}
			placeholderTextColor={palette.inkLow}
			secureTextEntry={secure}
			{...(secure ? { textContentType: "none", autoComplete: "off" } : null)}
			autoCapitalize={machine ? "none" : "sentences"}
			autoCorrect={!machine}
			spellCheck={!machine}
			returnKeyType={returnKeyType}
			onSubmitEditing={onSubmitEditing}
			submitBehavior={returnKeyType === "next" ? "submit" : undefined}
			ref={ref}
			allowFontScaling={allowFontScaling}
			style={{
				color: palette.inkHi,
				...(machine
					? { fontFamily: fonts.mono, fontSize: uiType.subheadline.fontSize * scale }
					: { fontSize: uiType.listRow.fontSize * scale }),
				minHeight: multiline ? 88 : 44,
				paddingHorizontal: space.rowInset,
				paddingVertical: space.rowPadding,
				textAlignVertical: multiline ? "top" : "center",
				opacity: disabled ? 0.4 : 1,
			}}
		/>
	);
}

/** A button, as the prototype draws one (styles.css .btn): `primary` is the
 * page's call to action, filled in the accent and full width at 50pt
 * (.btn.primary.big); the plain one is a 36pt capsule on the surface, beside
 * what it acts on (.btn), whose touch reaches the 44pt minimum (48 on
 * Android, as Action's does). Its label follows Dynamic Type, so the height
 * is a minimum. Most actions are rows; a page's one call to action is this.
 * It dims when pressed as the app's other buttons do. */
export function Button({ label, onPress, primary = false }: { label: string; onPress(): void; primary?: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const reach = ((Platform.OS === "android" ? 48 : 44) - 36) / 2;
	return (
		<Pressable
			accessibilityRole="button"
			onPress={onPress}
			hitSlop={primary ? undefined : { top: reach, bottom: reach }}
			style={({ pressed }) => ({
				minHeight: primary ? 50 : 36,
				borderRadius: primary ? 25 : 18,
				paddingHorizontal: primary ? 20 : 14,
				paddingVertical: 6,
				alignItems: "center",
				justifyContent: "center",
				alignSelf: primary ? "stretch" : "auto",
				backgroundColor: primary ? palette.accentFill : palette.surface,
				borderWidth: primary ? 0 : 0.5,
				borderColor: palette.edgeStrong,
				opacity: pressed ? 0.65 : 1,
			})}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{
					color: primary ? palette.onFill : palette.inkHi,
					fontWeight: "600",
					...scaledType(primary ? uiType.listRow : uiType.subheadline, scale),
				}}
			>
				{label}
			</Text>
		</Pressable>
	);
}

/** What went wrong with a form, at its top in danger ink. The Save that
 * caused it sits up in the header, so each new message is spoken: iOS has no
 * live region, so it's announced there; Android reads the polite live region
 * on its own, and announcing too would say it twice. */
export function FormError({ error }: { error: FormErrorReport | null }) {
	useEffect(() => {
		if (error && Platform.OS === "ios") AccessibilityInfo.announceForAccessibility(error.message);
	}, [error]);
	return error ? (
		<GroupFooter tone="danger" live>
			{error.message}
		</GroupFooter>
	) : null;
}

/** One refusal of a form's save. Each report is a new object, so a refusal
 * that repeats the last one's words still scrolls and speaks again. */
export interface FormErrorReport {
	message: string;
}

/** A form's error state: set a message (or null to clear it) as with
 * useState, and each message becomes its own FormErrorReport. */
export function useFormError(): [FormErrorReport | null, (message: string | null) => void] {
	const [error, setError] = useState<FormErrorReport | null>(null);
	const report = useCallback((message: string | null) => setError(message === null ? null : { message }), []);
	return [error, report];
}

/** A ref for a form's GroupedPage that scrolls back to the top, where its
 * FormError shows, each time a new error appears: a person scrolled down to
 * the last field who taps Save in the header would otherwise miss it. */
export function useErrorInView(error: FormErrorReport | null): RefObject<ScrollView | null> {
	const page = useRef<ScrollView>(null);
	useEffect(() => {
		if (error) page.current?.scrollTo({ y: 0, animated: true });
	}, [error]);
	return page;
}

/** A group's footer: ink-mid, or the attention or danger ink when it reports
 * something a person must act on. */
export function GroupFooter({
	children,
	tone = "normal",
	machine = false,
	live = false,
}: {
	children: string;
	tone?: "normal" | "attention" | "danger";
	/** A polite live region: Android reads it again when it changes. */
	live?: boolean;
	/** Text the machine wrote, such as an error the hub reported: Menlo. */
	machine?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const color = tone === "attention" ? palette.attentionInk : tone === "danger" ? palette.dangerInk : palette.inkMid;
	return (
		<Text
			allowFontScaling={allowFontScaling}
			accessibilityLiveRegion={live ? "polite" : undefined}
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
		<View style={{ flexDirection: "row", flexWrap: "wrap", alignItems: "center", gap: 6 }}>
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
