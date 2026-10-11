// The model sheet's list (spec 8.5): "Recent", then each provider's models,
// each row with its name, its context and price, the registry's notes, and a
// check on the current model. It is the sheet's one scroll view, so what
// stays in reach (search, Effort) lives in the sheet's header instead.
import type { ModelDescriptor, ModelListResponse } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { type ReactNode, useState } from "react";
import { Pressable, SectionList, Text, View } from "react-native";
import { type ModelPickerEntry, type ModelSection, modelSections } from "./modelPickerEntries";
import { allowFontScaling, useColors, useTextScale } from "./ui";

export function ModelPicker({
	catalog,
	loading,
	query,
	visionOnly,
	current,
	disabled,
	choose,
	header,
	capabilities = false,
}: {
	catalog: ModelListResponse | null;
	loading: boolean;
	query: string;
	visionOnly: boolean;
	/** The current model, "provider/model". */
	current: string;
	disabled: boolean;
	choose(model: ModelDescriptor): void;
	/** Rows above the models, such as the vision model's Session model and Off. */
	header?: ReactNode;
	/** Each row shows what its model can do: see images, use tools (spec 11). */
	capabilities?: boolean;
}) {
	const sections: ModelSection[] = catalog ? modelSections(catalog, query, visionOnly) : [];
	return (
		<SectionList
			sections={sections}
			keyboardShouldPersistTaps="handled"
			keyboardDismissMode="on-drag"
			stickySectionHeadersEnabled={false}
			contentContainerStyle={{ paddingBottom: 24 }}
			keyExtractor={(entry) => `${entry.model.provider}/${entry.model.model}`}
			ListHeaderComponent={
				<>
					{header}
					{catalog?.diagnostics?.length ? <CatalogNotices diagnostics={catalog.diagnostics} /> : null}
				</>
			}
			renderSectionHeader={({ section }) => <SectionTitle title={section.title} />}
			renderItem={({ item }) => (
				<ModelRow
					entry={item}
					selected={`${item.model.provider}/${item.model.model}` === current}
					disabled={disabled}
					capabilities={capabilities}
					onPress={() => choose(item.model)}
				/>
			)}
			ListEmptyComponent={
				loading || !catalog ? (
					<ModelSkeleton />
				) : (
					<Quiet>{query.trim() ? "No models match your search." : "No models are available for this session."}</Quiet>
				)
			}
		/>
	);
}

/** One choice with a check when it's the current one: a model row, or the
 * vision model's Session model and Off. */
export function ChoiceRow({
	title,
	selected,
	disabled,
	onPress,
	children,
	trailing,
}: {
	title: string;
	selected: boolean;
	disabled: boolean;
	onPress(): void;
	children?: ReactNode;
	/** Glyphs between the text and the check. */
	trailing?: ReactNode;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={title}
			accessibilityState={{ selected, disabled }}
			disabled={disabled}
			onPress={onPress}
			style={({ pressed }) => ({
				minHeight: 44,
				flexDirection: "row",
				alignItems: "center",
				gap: 12,
				paddingHorizontal: 16,
				paddingVertical: 8,
				opacity: disabled ? 0.4 : 1,
				backgroundColor: pressed ? palette.pressed : "transparent",
			})}
		>
			<View style={{ flex: 1, gap: 2 }}>
				<Text
					testID="model-title"
					allowFontScaling={allowFontScaling}
					style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale }}
				>
					{title}
				</Text>
				{children}
			</View>
			{trailing}
			{selected ? <SymbolView name="checkmark" tintColor={palette.accentInk} size={17 * scale} /> : null}
		</Pressable>
	);
}

function ModelRow({
	entry,
	selected,
	disabled,
	capabilities,
	onPress,
}: {
	entry: ModelPickerEntry;
	selected: boolean;
	disabled: boolean;
	capabilities: boolean;
	onPress(): void;
}) {
	return (
		<ChoiceRow
			title={entry.title}
			selected={selected}
			disabled={disabled}
			onPress={onPress}
			trailing={capabilities ? <Capabilities model={entry.model} /> : null}
		>
			{entry.detail ? <Caption>{entry.detail}</Caption> : null}
			{entry.warnings.map((warning) => (
				<Caption key={warning} warning>
					{warning}
				</Caption>
			))}
		</ChoiceRow>
	);
}

/** What a model can do, as bare glyphs in ink-mid, each named for VoiceOver. */
function Capabilities({ model }: { model: ModelDescriptor }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<>
			{model.supportsVision ? (
				<SymbolView name="eye" accessibilityLabel="Sees images" tintColor={palette.inkMid} size={15 * scale} />
			) : null}
			{model.supportsTools ? (
				<SymbolView
					name="wrench.and.screwdriver"
					accessibilityLabel="Uses tools"
					tintColor={palette.inkMid}
					size={15 * scale}
				/>
			) : null}
		</>
	);
}

function Caption({ children, warning = false }: { children: string; warning?: boolean }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				color: warning ? palette.attentionInk : palette.inkMid,
				fontSize: 13 * scale,
				lineHeight: 18 * scale,
				fontVariant: ["tabular-nums"],
			}}
		>
			{children}
		</Text>
	);
}

function SectionTitle({ title }: { title: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			testID="section-title"
			accessibilityRole="header"
			allowFontScaling={allowFontScaling}
			style={{
				paddingHorizontal: 16,
				paddingTop: 16,
				paddingBottom: 4,
				color: palette.inkMid,
				fontSize: 12 * scale,
				lineHeight: 16 * scale,
				fontWeight: "600",
				letterSpacing: 12 * 0.06,
				textTransform: "uppercase",
			}}
		>
			{title}
		</Text>
	);
}

/** The catalog's notices (a provider it couldn't list), folded under one
 * line until asked for. */
function CatalogNotices({ diagnostics }: { diagnostics: NonNullable<ModelListResponse["diagnostics"]> }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const [open, setOpen] = useState(false);
	return (
		<View style={{ paddingHorizontal: 16, paddingTop: 8, gap: 4 }}>
			<Pressable
				accessibilityRole="button"
				accessibilityState={{ expanded: open }}
				onPress={() => setOpen((shown) => !shown)}
				style={{ minHeight: 44, justifyContent: "center" }}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.accentInk, fontSize: 15 * scale, lineHeight: 20 * scale }}
				>
					{`Catalog notices (${diagnostics.length})`}
				</Text>
			</Pressable>
			{open
				? diagnostics.map((diagnostic) => (
						<Caption key={JSON.stringify(diagnostic)}>
							{[diagnostic.provider, diagnostic.message, diagnostic.hint].filter(Boolean).join(" · ")}
						</Caption>
					))
				: null}
		</View>
	);
}

const SKELETON_ROWS = [
	{ id: "first", width: "60%" },
	{ id: "second", width: "45%" },
	{ id: "third", width: "60%" },
	{ id: "fourth", width: "45%" },
] as const;

/** Stand-in rows while the models load: the list's shape, never a spinner. */
function ModelSkeleton() {
	const { palette } = useColors();
	return (
		<View style={{ paddingHorizontal: 16, paddingTop: 16, gap: 20 }}>
			{SKELETON_ROWS.map((row) => (
				<View key={row.id} testID="model-skeleton" style={{ gap: 6 }}>
					<View style={{ width: row.width, height: 14, borderRadius: 4, backgroundColor: palette.edge }} />
					<View style={{ width: "35%", height: 10, borderRadius: 4, backgroundColor: palette.edge }} />
				</View>
			))}
		</View>
	);
}

function Quiet({ children }: { children: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				paddingHorizontal: 16,
				paddingTop: 16,
				color: palette.inkMid,
				fontSize: 15 * scale,
				lineHeight: 20 * scale,
			}}
		>
			{children}
		</Text>
	);
}
