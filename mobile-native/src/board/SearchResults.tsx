import type { NavigationProjectSummary, SearchResult } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { stateWord } from "./attention";
import { BandHeader, Hairline, spokenAge } from "./BoardRow";
import { type SearchScope, type SearchSnapshot, searchResultMark, sessionResults } from "./boardSearch";
import { projectName } from "./ProjectTreeRow";
import { markFor, StateMark } from "./StateMark";

export interface SearchResultsProps {
	search: SearchSnapshot;
	scope: SearchScope;
	onScope: (scope: SearchScope) => void;
	connected: boolean;
	/** The recent searches, shown while the field is empty. */
	recent: string[];
	onOpen: (result: SearchResult) => void;
	onRecent: (query: string) => void;
	onClearRecent: () => void;
	/** The loaded projects the query matches (projectResults). */
	projects: readonly NavigationProjectSummary[];
	onOpenProject: (project: NavigationProjectSummary) => void;
}

const SCOPES: Array<{ scope: SearchScope; name: string }> = [
	{ scope: "all", name: "All" },
	{ scope: "live", name: "Live" },
];

/** What the Board shows under its search field while you search (spec
 * 7.4): the scope chips, then recent searches for an empty field, or for a
 * query the Sessions group and then, in All, the Projects group. */
export function SearchResults(props: SearchResultsProps) {
	return (
		<>
			<Scopes scope={props.scope} onScope={props.onScope} />
			{props.search.query ? <Found {...props} /> : <Recent {...props} />}
		</>
	);
}

function Recent({ recent, onRecent, onClearRecent }: SearchResultsProps) {
	const { palette } = useColors();
	const scale = useTextScale();
	if (!recent.length) return null;
	return (
		<>
			<View style={{ flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingRight: 16 }}>
				<BandHeader text="RECENT" />
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Clear recent searches"
					onPress={onClearRecent}
					// The action draws 30pt tall; the slop makes a 44pt target.
					hitSlop={{ top: 7, bottom: 7 }}
					style={({ pressed }) => ({
						minHeight: 30,
						marginTop: 16,
						justifyContent: "center",
						opacity: pressed ? 0.6 : 1,
					})}
				>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 15 * scale, fontWeight: "600", color: palette.accentInk }}
					>
						Clear
					</Text>
				</Pressable>
			</View>
			{recent.map((query, index) => (
				<View key={query}>
					{index > 0 ? <Hairline /> : null}
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={`Search for ${query}`}
						onPress={() => onRecent(query)}
						style={({ pressed }) => ({
							minHeight: 48,
							paddingHorizontal: 16,
							flexDirection: "row",
							alignItems: "center",
							columnGap: 10,
							backgroundColor: pressed ? palette.pressed : palette.page,
						})}
					>
						<View style={{ width: 28, alignItems: "center" }}>
							<SymbolView name="magnifyingglass" size={15 * scale} tintColor={palette.inkLow} />
						</View>
						<Text
							allowFontScaling={allowFontScaling}
							numberOfLines={1}
							style={{ flex: 1, fontSize: 17 * scale, color: palette.inkHi }}
						>
							{query}
						</Text>
					</Pressable>
				</View>
			))}
		</>
	);
}

function Found(props: SearchResultsProps) {
	const { scope, projects, onOpenProject } = props;
	// The projects are the Board's own, so they show whatever the hub's search
	// is doing. Live narrows to live sessions, which projects are not.
	return (
		<>
			<Sessions {...props} />
			{scope === "all" && projects.length ? (
				<>
					<BandHeader text={`PROJECTS · ${projects.length}`} />
					{projects.map((project, index) => (
						<View key={project.key}>
							{index > 0 ? <Hairline /> : null}
							<ProjectResultRow project={project} onOpen={onOpenProject} />
						</View>
					))}
				</>
			) : null}
		</>
	);
}

function Sessions({ search, scope, connected, onOpen }: SearchResultsProps) {
	if (search.failed) return <Note text="Couldn't search this hub's sessions." />;
	if (search.results) {
		const rows = sessionResults(search.results, scope);
		return rows.length ? (
			<>
				<BandHeader text={`SESSIONS · ${rows.length}`} />
				{rows.map((result, index) => (
					<View key={result.ref}>
						{index > 0 ? <Hairline /> : null}
						<ResultRow result={result} connected={connected} onOpen={onOpen} />
					</View>
				))}
			</>
		) : (
			<Note text="No sessions match." />
		);
	}
	if (!connected) return <Note text="Search works when the hub is connected." />;
	return search.searching ? <Note text="Searching…" /> : null;
}

function Scopes({ scope, onScope }: { scope: SearchScope; onScope: (scope: SearchScope) => void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ flexDirection: "row", columnGap: 8, paddingHorizontal: 16, paddingVertical: 8 }}>
			{SCOPES.map((chip) => {
				const selected = chip.scope === scope;
				return (
					<Pressable
						key={chip.scope}
						accessibilityRole="button"
						accessibilityLabel={chip.name}
						accessibilityState={{ selected }}
						onPress={() => onScope(chip.scope)}
						// The chip draws 32pt tall; the slop makes a 44pt target.
						hitSlop={{ top: 6, bottom: 6 }}
						style={({ pressed }) => ({
							minHeight: 32,
							paddingHorizontal: 14,
							borderRadius: 16,
							justifyContent: "center",
							backgroundColor: pressed ? palette.pressed : selected ? palette.accentBg : palette.surface,
							borderWidth: 0.5,
							borderColor: palette.edge,
						})}
					>
						<Text
							allowFontScaling={allowFontScaling}
							style={{ fontSize: 14 * scale, fontWeight: "600", color: selected ? palette.accentInk : palette.inkHi }}
						>
							{chip.name}
						</Text>
					</Pressable>
				);
			})}
		</View>
	);
}

/** The row every search result sits in, session or project. Its height
 * mirrors the Board row it stands in for: a session a signal row (64), a
 * project a quiet row (48). */
function resultRowStyle(palette: ReturnType<typeof useColors>["palette"], minHeight: number) {
	return ({ pressed }: { pressed: boolean }) => ({
		minHeight,
		paddingHorizontal: 16,
		paddingVertical: 8,
		flexDirection: "row" as const,
		alignItems: "center" as const,
		columnGap: 10,
		backgroundColor: pressed ? palette.pressed : palette.page,
	});
}

/** A result's second line: the session's project or the project's folder. */
function ResultDetail({ text }: { text: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			numberOfLines={1}
			ellipsizeMode="middle"
			style={{ marginTop: 2, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow }}
		>
			{text}
		</Text>
	);
}

function ResultRow({
	result,
	connected,
	onOpen,
}: {
	result: SearchResult;
	connected: boolean;
	onOpen: (result: SearchResult) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const state = searchResultMark(result);
	const title = result.title || "Untitled session";
	// A state that draws no mark (Idle, Shut down) goes unnamed in the label too.
	const word = markFor(state, false) ? stateWord(state) : null;
	const label = [title, word, result.project, result.age && spokenAge(result.age)].filter(Boolean).join(", ");
	return (
		<Pressable
			testID="search-result"
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={() => onOpen(result)}
			style={resultRowStyle(palette, 64)}
		>
			<StateMark state={state} connected={connected} />
			<View style={{ flex: 1, minWidth: 0 }}>
				<View style={{ flexDirection: "row", alignItems: "center", columnGap: 8 }}>
					<Text
						allowFontScaling={allowFontScaling}
						numberOfLines={1}
						style={{ flex: 1, fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: palette.inkHi }}
					>
						{title}
					</Text>
					{result.age ? (
						<Text
							allowFontScaling={allowFontScaling}
							style={{ fontSize: 13 * scale, color: palette.inkLow, fontVariant: ["tabular-nums"] }}
						>
							{result.age}
						</Text>
					) : null}
				</View>
				{result.project ? <ResultDetail text={result.project} /> : null}
			</View>
		</Pressable>
	);
}

function ProjectResultRow({
	project,
	onOpen,
}: {
	project: NavigationProjectSummary;
	onOpen: (project: NavigationProjectSummary) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const name = projectName(project);
	const folder = project.working_dir && project.working_dir !== name ? project.working_dir : null;
	return (
		<Pressable
			testID="project-result"
			accessibilityRole="button"
			accessibilityLabel={[name, "project", folder].filter(Boolean).join(", ")}
			onPress={() => onOpen(project)}
			style={resultRowStyle(palette, 48)}
		>
			<View style={{ width: 28, alignItems: "center" }}>
				<SymbolView name="folder" size={17 * scale} tintColor={palette.inkMid} />
			</View>
			<View style={{ flex: 1, minWidth: 0 }}>
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: palette.inkHi }}
				>
					{name}
				</Text>
				{folder ? <ResultDetail text={folder} /> : null}
			</View>
		</Pressable>
	);
}

function Note({ text }: { text: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				paddingHorizontal: 16,
				paddingVertical: 24,
				fontSize: 15 * scale,
				lineHeight: 20 * scale,
				color: palette.inkMid,
			}}
		>
			{text}
		</Text>
	);
}
