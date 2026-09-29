// New session's plugin checklist (spec 11): the chosen host's plugins grouped
// by marketplace (as typed, in Menlo), each with what it brings, its warnings
// and any problem that blocks the start, and a switch. All and None set every
// switch at once. The list is the sheet's one plugin preview (sheetPlugins.ts).
import {
	type PluginLaunchCandidate,
	type PluginSelectionState,
	selectAllPlugins,
	selectNoPlugins,
	setPluginSelected,
	withPluginSelection,
} from "@evener/appwire-client";
import { useState } from "react";
import { Pressable, Switch, Text, View } from "react-native";
import { useStore } from "zustand";
import { Group, GroupedPage, GroupFooter, Row } from "../sheet/Grouped";
import { SearchField } from "../sheet/SearchField";
import { SheetStatus } from "../sheet/SheetStatus";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useNewSession } from "./newSessionContext";
import { pluginCounts, pluginGroups, pluginWarnings, unclaimedDiagnostics } from "./pluginFacts";
import { pluginChoice } from "./sheetPlugins";

export function PluginChecklist() {
	const { store, plugins, hostLabel } = useNewSession();
	const { source, cwd, launchOverrides, submitting, setLaunchOverrides } = useStore(store);
	const [query, setQuery] = useState("");
	const { selection, response, on, total, issues } = pluginChoice(launchOverrides, plugins);
	const change = (next: PluginSelectionState) => setLaunchOverrides(withPluginSelection(launchOverrides, next));
	const disabled = submitting || !response;
	// Problems naming a plugin with no row here (a chosen plugin the host no
	// longer has): said on the page, with a way to take just that one out.
	const listed = new Set(response?.plugins.map((plugin) => plugin.name));
	const orphans = issues.filter((issue) => !listed.has(issue.name));
	const explicit = selection.mode === "explicit" ? selection.names : [];
	const removable = orphans.map((issue) => issue.name).filter((name) => explicit.includes(name));
	const search = query.trim().toLowerCase();
	const groups = response
		? pluginGroups({
				...response,
				plugins: response.plugins.filter((plugin) =>
					`${plugin.name} ${plugin.description ?? ""} ${plugin.marketplace ?? ""}`.toLowerCase().includes(search),
				),
			})
		: [];
	return (
		<GroupedPage>
			<SheetStatus />
			<View style={{ paddingHorizontal: 16, paddingTop: 8, gap: 4 }}>
				<SearchField label="Search plugins" value={query} onChangeText={setQuery} />
				<View style={{ flexDirection: "row", gap: 8 }}>
					<TextButton label="All" disabled={disabled} onPress={() => response && change(selectAllPlugins(response))} />
					<TextButton label="None" disabled={disabled} onPress={() => change(selectNoPlugins())} />
				</View>
			</View>
			{plugins.status === "error" ? <GroupFooter tone="danger">{plugins.message}</GroupFooter> : null}
			{!cwd.trim() ? (
				<GroupFooter>Plugins are listed once a project is chosen.</GroupFooter>
			) : !response && plugins.status === "loading" ? (
				<GroupFooter>{`Checking plugins on ${hostLabel(source)}…`}</GroupFooter>
			) : null}
			{orphans.map((issue) => (
				<GroupFooter key={`issue:${issue.name}`} tone="danger">{`${issue.name}: ${issue.reason}`}</GroupFooter>
			))}
			{removable.length > 0 ? (
				<Group>
					{removable.map((name) => (
						<Row
							key={name}
							label={`Remove ${name}`}
							tone="accent"
							disabled={submitting}
							onPress={() => change({ mode: "explicit", names: explicit.filter((chosen) => chosen !== name) })}
						/>
					))}
				</Group>
			) : null}
			{(response ? unclaimedDiagnostics(response) : []).map((diagnostic) => (
				<GroupFooter key={diagnostic} tone="attention">
					{diagnostic}
				</GroupFooter>
			))}
			{groups.map((group) => (
				<View key={group.marketplace ?? ""}>
					<Group label={group.marketplace ?? "Other plugins"} machineLabel={Boolean(group.marketplace)}>
						{group.plugins.map((plugin) => (
							<PluginRow
								key={plugin.name}
								plugin={plugin}
								warnings={response ? pluginWarnings(response, plugin.name) : []}
								problem={issues.find((issue) => issue.name === plugin.name)?.reason ?? null}
								on={on.includes(plugin.name)}
								disabled={disabled}
								onChange={(next) => response && change(setPluginSelected(selection, response, plugin.name, next))}
							/>
						))}
					</Group>
				</View>
			))}
			{response ? (
				<GroupFooter>{`${on.length} of ${total} on. Plugins can't be changed after the session starts.`}</GroupFooter>
			) : null}
		</GroupedPage>
	);
}

/** A plugin: its name, its description, what it brings in ink-low, its
 * warnings in attention ink and a blocking problem in danger ink, and a switch
 * in the accent color (spec 16.1). The switch is the accessible element. */
function PluginRow({
	plugin,
	warnings,
	problem,
	on,
	disabled,
	onChange,
}: {
	plugin: PluginLaunchCandidate;
	warnings: string[];
	problem: string | null;
	on: boolean;
	disabled: boolean;
	onChange(on: boolean): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const line = (text: string, color: string, key?: string) => (
		<Text key={key} allowFontScaling={allowFontScaling} style={{ color, fontSize: 13 * scale, lineHeight: 18 * scale }}>
			{text}
		</Text>
	);
	const counts = pluginCounts(plugin);
	return (
		<View
			style={{
				flexDirection: "row",
				alignItems: "center",
				gap: 12,
				minHeight: 44,
				paddingHorizontal: 16,
				paddingVertical: 8,
			}}
		>
			<View style={{ flex: 1, gap: 2 }} accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale }}
				>
					{plugin.name}
				</Text>
				{plugin.description ? line(plugin.description, palette.inkMid) : null}
				{counts ? line(counts, palette.inkLow) : null}
				{warnings.map((warning) => line(warning, palette.attentionInk, warning))}
				{problem ? line(problem, palette.dangerInk) : null}
			</View>
			<Switch
				accessibilityLabel={plugin.name}
				accessibilityHint={[plugin.description, problem].filter(Boolean).join(". ") || undefined}
				value={on}
				disabled={disabled}
				onValueChange={onChange}
				trackColor={{ false: palette.edgeStrong, true: palette.accent }}
				ios_backgroundColor={palette.edgeStrong}
			/>
		</View>
	);
}

/** An accent text button in a 44pt target: All, None. */
function TextButton({ label, disabled, onPress }: { label: string; disabled: boolean; onPress(): void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ disabled }}
			disabled={disabled}
			onPress={onPress}
			style={{ minHeight: 44, minWidth: 44, justifyContent: "center", opacity: disabled ? 0.4 : 1 }}
		>
			<Text allowFontScaling={allowFontScaling} style={{ color: palette.accentInk, fontSize: 17 * scale }}>
				{label}
			</Text>
		</Pressable>
	);
}
