// The Commands and skills sheet (spec 8.5; rulings 15 and 37): the built-in
// commands this session can run, then its skills and plugin commands grouped
// by plugin. Choosing one closes the sheet and puts its "/name" at the start
// of the draft, ready for the command's argument. A formSheet route that
// opens at medium, as pickers do, with its search field in the header. The
// session screen provides what the session can run through commandHosts.
import {
	type CommandCatalogClient,
	createSessionCommandCatalog,
	type SessionCommandCatalogState,
	type SlashMenuItem,
	type SlashSpliceResult,
	spliceSlashCommand,
} from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { Pressable, SectionList, Text, TextInput, View } from "react-native";
import { builtinComposerItems, type ComposerCommandSession } from "../composerCommand";
import { useConnection } from "../ConnectionProvider";
import { fonts } from "../design/tokens";
import type { Routes } from "../screens";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { sheetHosts, sheetKey, useSheetHost } from "../sheet/sheetHosts";
import { allowFontScaling, searchFieldStyle, useColors, useTextScale } from "../ui";

export interface CommandsHost {
	session: ComposerCommandSession;
	/** Put the chosen "/name" into the session's draft. */
	choose(invocation: string): void;
}

export const commandHosts = sheetHosts<CommandsHost>();

/** The invocation at the start of the draft, one space after it unless the
 * draft already starts with whitespace, and what you typed kept after it. The
 * caret lands just after the invocation and its space. */
export function insertInvocation(draft: string, invocation: string): SlashSpliceResult {
	return spliceSlashCommand(draft, { start: 0, end: 0, query: "" }, invocation);
}

/** The built-in commands, in the spec's order. Each shows only when the
 * composer's registry offers it for this session. */
const COMMANDS = [
	{ id: "goal", name: "Goal", line: "An objective the agent pursues until it's done" },
	{ id: "compact", name: "Compact context", line: "Free up token space" },
	{ id: "aside", name: "Aside", line: "A side question in its own session; this one keeps working" },
	{ id: "tasks", name: "Tasks", line: "The session's task list" },
	{ id: "model", name: "Model", line: "Change the model" },
	{ id: "reasoning-effort", name: "Effort", line: "How long it thinks before acting" },
	{ id: "clear", name: "Clear", line: "Start fresh in this session" },
] as const;

/** The group for skills and commands that came with no plugin: your own. */
const YOURS = "Your commands and skills";

interface CommandRow {
	key: string;
	name: string;
	line: string;
	invocation: string;
}

interface CommandSection {
	title: string;
	/** A plugin's name, which reads as typed in Menlo. */
	plugin: boolean;
	data: CommandRow[];
}

/** The plugin an item came with, or "" for none. A plugin command's key is
 * "plugin:<pluginName>:<name>"; a plugin's skill is named "<plugin>:<name>". */
function pluginOf(item: SlashMenuItem): string {
	if (item.kind === "plugin") return item.key.split(":")[1] ?? "";
	const colon = item.label.indexOf(":");
	return colon > 0 ? item.label.slice(0, colon) : "";
}

function catalogRow(item: SlashMenuItem, plugin: string): CommandRow {
	const name = item.kind === "skill" && plugin ? item.label.slice(plugin.length + 1) : item.label;
	return { key: item.key, name, line: item.hint, invocation: item.invocation };
}

function commandSections(
	session: ComposerCommandSession,
	items: readonly SlashMenuItem[],
	query: string,
): CommandSection[] {
	const needle = query.trim().toLowerCase();
	const matches = (row: CommandRow) =>
		!needle || row.name.toLowerCase().includes(needle) || row.line.toLowerCase().includes(needle);
	const offered = new Set(builtinComposerItems(session).map((item) => item.key));
	const commands: CommandRow[] = COMMANDS.filter((command) => offered.has(`builtin:${command.id}`)).map((command) => ({
		key: command.id,
		name: command.name,
		line: command.line,
		invocation: `/${command.id}`,
	}));
	const groups = new Map<string, CommandRow[]>();
	for (const item of items) {
		const plugin = pluginOf(item);
		const group = groups.get(plugin) ?? [];
		groups.set(plugin, group);
		group.push(catalogRow(item, plugin));
	}
	// Plugins in name order so a plugin is where you look for it; your own last.
	const plugins = [...groups.keys()].filter((plugin) => plugin !== "").sort();
	const sections: CommandSection[] = [
		{ title: "Commands", plugin: false, data: commands },
		...plugins.map((plugin) => ({ title: plugin, plugin: true, data: groups.get(plugin) ?? [] })),
		{ title: YOURS, plugin: false, data: groups.get("") ?? [] },
	];
	return sections
		.map((section) => ({ ...section, data: section.data.filter(matches) }))
		.filter((section) => section.data.length > 0);
}

const NO_CATALOG: SessionCommandCatalogState = { items: [], loading: false, error: null };
const noSubscription = () => () => {};
const noCatalog = () => NO_CATALOG;

/** The session's skills and plugin commands, read while the sheet is open.
 * Without the hub there is no catalog, and the sheet offers the commands. */
function useSessionCatalog(client: CommandCatalogClient | null, ref: string): SessionCommandCatalogState {
	const catalog = useMemo(() => (client ? createSessionCommandCatalog(client, ref) : null), [client, ref]);
	const state = useSyncExternalStore(catalog?.subscribe ?? noSubscription, catalog?.getState ?? noCatalog);
	useEffect(() => {
		if (!catalog) return;
		catalog.start();
		return () => catalog.dispose();
	}, [catalog]);
	return state;
}

export function CommandsSheet({ route, navigation }: NativeStackScreenProps<Routes, "CommandsSheet">) {
	const { hubId, ref } = route.params;
	const sheet = useSheet();
	const host = useSheetHost(commandHosts, sheetKey(hubId, ref), sheet);
	const { activeProfile, client, state } = useConnection();
	const catalog = useSessionCatalog(activeProfile?.id === hubId && state === "ready" ? client : null, ref);
	const [query, setQuery] = useState("");
	const { palette } = useColors();
	const scale = useTextScale();
	if (!host) return null;
	const sections = commandSections(host.session, catalog.error ? [] : catalog.items, query);
	const choose = (invocation: string) =>
		sheet.finish(() => {
			navigation.goBack();
			host.choose(invocation);
		});
	const caption = { fontSize: 13 * scale, lineHeight: 18 * scale };
	return (
		<Sheet
			title="Commands and skills"
			done={{ onPress: () => sheet.finish() }}
			accessory={
				<View style={{ paddingHorizontal: 16, paddingBottom: 8 }}>
					<TextInput
						accessibilityLabel="Search commands and skills"
						value={query}
						onChangeText={setQuery}
						placeholder="Search"
						placeholderTextColor={palette.inkLow}
						autoCapitalize="none"
						autoCorrect={false}
						clearButtonMode="while-editing"
						allowFontScaling={allowFontScaling}
						style={searchFieldStyle(palette, scale)}
					/>
				</View>
			}
		>
			<SectionList
				sections={sections}
				keyboardShouldPersistTaps="handled"
				keyboardDismissMode="on-drag"
				stickySectionHeadersEnabled={false}
				contentContainerStyle={{ paddingBottom: 24 }}
				keyExtractor={(row) => row.key}
				renderSectionHeader={({ section }) => (
					<Text
						testID="commands-header"
						accessibilityRole="header"
						allowFontScaling={allowFontScaling}
						style={{
							paddingHorizontal: 16,
							paddingTop: 16,
							paddingBottom: 4,
							color: palette.inkMid,
							...(section.plugin
								? { fontFamily: fonts.mono, fontSize: 12 * scale, lineHeight: 16 * scale }
								: { ...caption, fontWeight: "600" as const }),
						}}
					>
						{section.title}
					</Text>
				)}
				renderItem={({ item }) => (
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={item.name}
						accessibilityHint={item.line || undefined}
						onPress={() => choose(item.invocation)}
						style={({ pressed }) => ({
							minHeight: 44,
							justifyContent: "center",
							gap: 2,
							paddingHorizontal: 16,
							paddingVertical: 8,
							backgroundColor: pressed ? palette.pressed : "transparent",
						})}
					>
						<Text
							testID="commands-name"
							allowFontScaling={allowFontScaling}
							style={{ color: palette.inkHi, fontSize: 17 * scale, lineHeight: 22 * scale }}
						>
							{item.name}
						</Text>
						{item.line ? (
							<Text allowFontScaling={allowFontScaling} style={{ ...caption, color: palette.inkMid }}>
								{item.line}
							</Text>
						) : null}
					</Pressable>
				)}
				ListEmptyComponent={
					<Text
						allowFontScaling={allowFontScaling}
						style={{ paddingHorizontal: 16, paddingTop: 16, color: palette.inkMid, ...caption }}
					>
						No commands or skills match your search.
					</Text>
				}
				ListFooterComponent={
					catalog.error ? (
						<Text
							accessibilityRole="alert"
							allowFontScaling={allowFontScaling}
							numberOfLines={1}
							style={{ paddingHorizontal: 16, paddingTop: 16, color: palette.dangerInk, ...caption }}
						>
							{catalog.error}
						</Text>
					) : catalog.loading && catalog.items.length === 0 ? (
						<CatalogSkeleton />
					) : null
				}
			/>
		</Sheet>
	);
}

const SKELETON_ROWS = [
	{ id: "first", width: "45%" },
	{ id: "second", width: "60%" },
	{ id: "third", width: "45%" },
] as const;

/** Stand-in rows while the skills load: the list's shape, never a spinner. */
function CatalogSkeleton() {
	const { palette } = useColors();
	return (
		<View style={{ paddingHorizontal: 16, paddingTop: 16, gap: 20 }}>
			{SKELETON_ROWS.map((row) => (
				<View key={row.id} testID="commands-skeleton" style={{ gap: 6 }}>
					<View style={{ width: row.width, height: 14, borderRadius: 4, backgroundColor: palette.edge }} />
					<View style={{ width: "70%", height: 10, borderRadius: 4, backgroundColor: palette.edge }} />
				</View>
			))}
		</View>
	);
}
