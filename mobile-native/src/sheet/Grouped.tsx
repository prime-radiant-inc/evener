// The grouped list the Hub and New session sheets are built from:
// sentence-case section labels, inset groups of rows on the surface color over
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
import {
	AccessibilityInfo,
	Platform,
	Pressable,
	ScrollView,
	Switch,
	Text,
	TextInput,
	View,
	useWindowDimensions,
} from "react-native";
import { fonts, type Palette, scaledType, space, uiType } from "../design/tokens";
import { haptic } from "../haptics";
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
			keyboardDismissMode="on-drag"
			automaticallyAdjustKeyboardInsets={Platform.OS === "ios"}
		>
			{children}
		</ScrollView>
	);
}

/** A sentence-case section heading. A machine label (a marketplace or
 * provider profile) is Menlo as typed. A group takes its label as Group's `label`; this
 * heads anything else, such as a segmented control or a footer. */
export function GroupLabel({
	children,
	machine = false,
	selectable = false,
}: {
	children: string;
	machine?: boolean;
	selectable?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const { fontSize, lineHeight, ...sectionCase } = scaledType(uiType.sectionLabel, scale);
	return (
		<Text
			accessibilityRole="header"
			selectable={selectable}
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
	selectableLabel = false,
	section,
}: {
	children: ReactNode;
	label?: string;
	/** Which group this is, as a stable token: its `group:<section>` testID. */
	section?: string;
	/** The label is a marketplace or similar machine name (see GroupLabel). */
	machineLabel?: boolean;
	/** A user-authored identity that can be selected and copied. */
	selectableLabel?: boolean;
}) {
	const { palette } = useColors();
	const rows = Children.toArray(children);
	const group = (
		<View
			testID={section ? `group:${section}` : undefined}
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
			<GroupLabel machine={machineLabel} selectable={selectableLabel}>
				{label}
			</GroupLabel>
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
	/** Names the state the row reports, so a test can read it without its words. */
	testID?: string;
	/** Preserve native selection for displayed settings and identifiers. */
	selectable?: boolean;
	/** A control of its own at the row's end, such as a mini button
	 * ("Install"): outside what a press on the row opens, so each is its own
	 * target and its own VoiceOver element. */
	accessory?: ReactNode;
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
	selectable = false,
	children,
}: {
	label: string;
	sub?: string;
	subTone?: "normal" | "danger";
	machineLabel?: boolean;
	machineSub?: boolean;
	/** A row's action or destructive ink overrides the default inkHi. */
	labelColor?: string;
	selectable?: boolean;
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
					selectable={selectable}
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
					selectable={selectable}
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
	testID,
	selectable = false,
	accessory,
}: RowProps) {
	const { palette } = useColors();
	let labelColor = palette.inkHi;
	if (disabled) {
		labelColor = palette.inkMid;
	} else if (tone === "accent") {
		labelColor = palette.accentInk;
	} else if (tone === "danger") {
		labelColor = palette.dangerInk;
	}
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
					selectable={selectable}
				>
					{rendersSomething(value) ? (
						<View testID="row-value" style={{ flexShrink: 1 }}>
							{plainValue !== undefined ? <RowValue text={plainValue} selectable={selectable} /> : value}
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
	} as const;
	const state = checked === undefined ? { disabled } : { disabled, selected: checked };
	// With a control at its end, the row's own part takes the rest of the
	// width and the control sits beside it, inset like the row's text.
	const own = accessory ? [style, { flex: 1, paddingRight: 0 }] : style;
	const row = !onPress ? (
		<View accessible accessibilityLabel={reading} accessibilityState={state} testID={testID} style={own}>
			{body}
		</View>
	) : (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={reading}
			accessibilityState={state}
			testID={testID}
			disabled={disabled}
			onPress={onPress}
			style={({ pressed }) => [own, pressed ? { backgroundColor: palette.pressed } : null]}
		>
			{body}
		</Pressable>
	);
	if (!accessory) return row;
	return (
		<View style={{ flexDirection: "row", alignItems: "center" }}>
			{row}
			{/* Stretched to the row's height: React Native clips a touch at its
			    parent's bounds, so a control's reach needs the row's room. */}
			<View
				testID="row-accessory"
				style={{ alignSelf: "stretch", justifyContent: "center", paddingHorizontal: space.rowInset }}
			>
				{accessory}
			</View>
		</View>
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
			<RowText
				label={label}
				sub={sub}
				subTone={subTone}
				labelColor={disabled && !onPress ? palette.inkMid : undefined}
			/>
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
			}}
		>
			{icon ? <Glyph name={icon} color={palette.inkMid} /> : null}
			{onPress ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={accessibilityLabel ?? [label, sub].filter(Boolean).join(", ")}
					onPress={onPress}
					// Pressed, it shades as a Row does.
					style={({ pressed }) => ({
						flex: 1,
						alignSelf: "stretch",
						justifyContent: "center",
						backgroundColor: pressed ? palette.pressed : undefined,
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
				color: disabled ? palette.inkMid : palette.inkHi,
				// React Native draws a TextInput's placeholder in the input's
				// own font, so a machine field shows hint words in the UI font
				// while empty and its value in Menlo once there is one (spec
				// 16.2).
				...(machine && value !== "" ? { fontFamily: fonts.mono } : null),
				fontSize: (machine ? uiType.subheadline : uiType.listRow).fontSize * scale,
				minHeight: multiline ? 88 : 44,
				paddingHorizontal: space.rowInset,
				paddingVertical: space.rowPadding,
				textAlignVertical: multiline ? "top" : "center",
			}}
		/>
	);
}

/** The platform's minimum touch target, read at render so it follows
 * Platform.OS. */
const minimumTarget = () => (Platform.OS === "android" ? 48 : 44);

/** Each Button kind, from the prototype's .btn.primary.big, .btn and
 * .mini-btn. `reach` is how far the touch extends: a primary or plain button
 * reaches the platform's minimum target, and a mini button, which sits inside
 * a 44pt row, reaches the row's edges and no further. */
const BUTTON_KINDS = {
	primary: {
		drawn: 50,
		reach: "platform",
		shape: { borderRadius: 25, paddingHorizontal: 20, paddingVertical: 6, alignSelf: "stretch" },
		fill: "accentFill",
		ink: "onFill",
		type: uiType.listRow,
		pressed: "dim",
	},
	plain: {
		drawn: 36,
		reach: "platform",
		shape: { borderRadius: 18, paddingHorizontal: 14, paddingVertical: 6, borderWidth: 0.5 },
		fill: "surface",
		ink: "inkHi",
		type: uiType.subheadline,
		pressed: "dim",
	},
	mini: {
		drawn: 30,
		reach: "row",
		shape: { borderRadius: 8, paddingHorizontal: 8, paddingVertical: 6 },
		fill: undefined,
		ink: "accentInk",
		type: uiType.footnote,
		pressed: "shade",
	},
} as const satisfies Record<
	string,
	{
		drawn: number;
		reach: "platform" | "row";
		shape: object;
		fill: keyof Palette | undefined;
		ink: keyof Palette;
		type: { fontSize: number; lineHeight: number };
		pressed: "dim" | "shade";
	}
>;

/** A button, as the prototype draws one (styles.css):
 * - `primary`, the page's call to action: filled in the accent and full width
 *   at 50pt (.btn.primary.big). It dims when pressed, as the app's other
 *   buttons do.
 * - plain, beside what it acts on: a 36pt capsule on the surface (.btn),
 *   dimming when pressed.
 * - `mini`, a row's own control such as Install (.mini-btn): 13pt accent text
 *   with no fill, shaded when pressed.
 * - `text`, an inline action: 16pt accent text with no fill, drawn at the
 *   platform's minimum target; with `quiet`, the same in the secondary ink at
 *   regular weight, for an action that steps back.
 * - `primary` with `compact`: the call to action sized to its label, a 16pt
 *   accent pill at the platform's minimum target, for one that sits beside
 *   other controls rather than across the page.
 * A plain, text or compact primary button's touch reaches the 44pt minimum (48 on Android); a
 * mini button's reaches its 44pt row's edges. Its label follows Dynamic Type,
 * so the height is a minimum. `expanded` tells VoiceOver whether the section
 * a disclosure button opens is open.
 * Most actions are rows; a page's one call to action is `primary`. */
export function Button({
	label,
	onPress,
	primary = false,
	mini = false,
	text = false,
	quiet = false,
	compact = false,
	disabled = false,
	expanded,
	accessibilityLabel,
}: {
	label: string;
	onPress(): void;
	primary?: boolean;
	mini?: boolean;
	text?: boolean;
	/** With `text`: the secondary ink at regular weight. */
	quiet?: boolean;
	/** With `primary`: a pill sized to its label instead of full width. */
	compact?: boolean;
	disabled?: boolean;
	expanded?: boolean;
	/** VoiceOver's name when the label alone doesn't say what it acts on, as
	 * for a row's "Install" ("Install tool from acme"). */
	accessibilityLabel?: string;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	if (text || (primary && compact)) {
		const target = minimumTarget();
		const compactPrimary = !text;
		return (
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={accessibilityLabel ?? label}
				accessibilityState={{ disabled, expanded }}
				disabled={disabled}
				onPress={onPress}
				style={({ pressed }) => ({
					minWidth: target,
					minHeight: target,
					justifyContent: "center",
					paddingHorizontal: compactPrimary ? 18 : 8,
					paddingVertical: 8,
					...(compactPrimary ? { backgroundColor: palette.accentFill, borderRadius: 24 } : null),
					opacity: disabled ? 0.4 : pressed ? 0.65 : 1,
				})}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{
						color: compactPrimary ? palette.onFill : quiet ? palette.inkMid : palette.accentInk,
						fontSize: 16 * scale,
						fontWeight: quiet && !compactPrimary ? "400" : "600",
						flexShrink: 1,
					}}
				>
					{label}
				</Text>
			</Pressable>
		);
	}
	const kind = BUTTON_KINDS[primary ? "primary" : mini ? "mini" : "plain"];
	const target = kind.reach === "row" ? 44 : minimumTarget();
	const reach = (target - kind.drawn) / 2;
	const fill = kind.fill && palette[kind.fill];
	const outline = "borderWidth" in kind.shape ? { borderColor: palette.edgeStrong } : null;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={accessibilityLabel}
			accessibilityState={{ disabled, expanded }}
			disabled={disabled}
			onPress={onPress}
			hitSlop={reach > 0 ? { top: reach, bottom: reach } : undefined}
			style={({ pressed }) => ({
				minHeight: kind.drawn,
				alignItems: "center",
				justifyContent: "center",
				...kind.shape,
				...outline,
				backgroundColor: pressed && kind.pressed === "shade" ? palette.pressed : fill,
				opacity: disabled ? 0.4 : pressed && kind.pressed === "dim" ? 0.65 : 1,
			})}
		>
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette[kind.ink], fontWeight: "600", ...scaledType(kind.type, scale) }}
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
		<GroupFooter tone="danger" live testID={error.reason ? `form-error-${error.reason}` : "form-error"}>
			{error.message}
		</GroupFooter>
	) : null;
}

/** One refusal of a form's save. Each report is a new object, so a refusal
 * that repeats the last one's words still scrolls and speaks again. */
export interface FormErrorReport {
	message: string;
	/** Why the save was refused, when the form tells refusals apart, such as "write-busy". */
	reason?: string;
}

/** A form's error state: set a message (or null to clear it) as with
 * useState, and each message becomes its own FormErrorReport. */
export function useFormError(): [FormErrorReport | null, (message: string | null, reason?: string) => void] {
	const [error, setError] = useState<FormErrorReport | null>(null);
	const report = useCallback(
		(message: string | null, reason?: string) => setError(message === null ? null : { message, reason }),
		[],
	);
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
	selectable = false,
	testID,
}: {
	children: string;
	tone?: "normal" | "attention" | "danger";
	/** A polite live region: Android reads it again when it changes. */
	live?: boolean;
	/** Text the machine wrote, such as an error the hub reported: Menlo. */
	machine?: boolean;
	/** Preserve selection when the diagnostic includes user-supplied values. */
	selectable?: boolean;
	/** Names what the footer reports, so a test can tell one report from another. */
	testID?: string;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const color = tone === "attention" ? palette.attentionInk : tone === "danger" ? palette.dangerInk : palette.inkMid;
	return (
		<Text
			allowFontScaling={allowFontScaling}
			selectable={selectable}
			accessibilityLiveRegion={live ? "polite" : undefined}
			testID={testID}
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

/** Compact choices become checked rows when their actual width or text size
 * needs more room. Labels may wrap in either layout; selection never shrinks
 * the person's preferred type. A tap on the chosen segment does nothing. */
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
	const { width: windowWidth } = useWindowDimensions();
	const [measuredWidth, setMeasuredWidth] = useState<number | null>(null);
	const width = measuredWidth ?? windowWidth - space.margin * 2;
	const stacked = scale > 1.2 || width / options.length < 88;
	const selectedSurface = palette.scheme === "dark" ? palette.pressed : palette.surface;
	return (
		<View
			accessibilityRole="radiogroup"
			accessibilityLabel={label}
			onLayout={(event) => setMeasuredWidth(event.nativeEvent.layout.width)}
			style={{
				flexDirection: stacked ? "column" : "row",
				gap: 2,
				marginHorizontal: space.margin,
				padding: 2,
				borderRadius: 12,
				backgroundColor: palette.inset,
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
							if (!disabled && !chosen) {
								haptic("selection");
								onChange(option.value);
							}
						}}
						style={{
							flex: stacked ? undefined : 1,
							flexDirection: "row",
							minHeight: 44,
							alignItems: "center",
							justifyContent: stacked ? "flex-start" : "center",
							gap: space.rowGap,
							paddingHorizontal: 12,
							paddingVertical: 8,
							borderRadius: 10,
							backgroundColor: chosen ? selectedSurface : "transparent",
						}}
					>
						{stacked ? (
							<Glyph name={chosen ? "checkmark" : undefined} color={disabled ? palette.inkMid : palette.accentInk} />
						) : null}
						<Text
							allowFontScaling={allowFontScaling}
							style={{
								flexShrink: 1,
								textAlign: stacked ? "left" : "center",
								color: disabled ? palette.inkMid : palette.inkHi,
								...scaledType(uiType.subheadline, scale),
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
				// The prototype's .tag (styles.css): 11pt semibold on a 16pt line.
				fontSize: 11 * scale,
				lineHeight: 16 * scale,
				fontWeight: "600",
				paddingHorizontal: 6,
				paddingVertical: 1,
				borderRadius: 6,
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
	selectable = false,
}: {
	text?: string;
	tag?: { text: string; tone: keyof typeof TAG_TONES } | null;
	/** "attention" for a value that may need a human, such as Offline. */
	tone?: "normal" | "attention";
	selectable?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ flexDirection: "row", flexWrap: "wrap", alignItems: "center", gap: 6 }}>
			{text ? (
				<Text
					allowFontScaling={allowFontScaling}
					selectable={selectable}
					style={{
						flexShrink: 1,
						color: tone === "attention" ? palette.attentionInk : palette.inkMid,
						// The prototype's .gv: a value sits a step under the 17pt label.
						...scaledType(uiType.subheadline, scale),
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
