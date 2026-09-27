// One row of the Board's Projects, Test runs or Archived section (spec 7.1):
// a host, a project, a host branch inside a project, a tier's caption, the
// Archived group, a "more" row, or a placeholder, and the section's header.
// Sessions draw as quiet BoardRows instead.
import type { NavigationProjectSummary } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import type { ReactElement, ReactNode } from "react";
import { Platform, Pressable, Text, View } from "react-native";
import { useColors, useTextScale } from "../ui";
import { plural } from "./attention";
import type { OrganizeBy } from "./boardMemory";
import { bandHeaderText, FoldChevron } from "./BoardRow";
import type { ProjectTreeItem } from "./projectTree";

export type ProjectRowItem = Exclude<ProjectTreeItem, { kind: "session" }>;

export interface ProjectTreeRowProps {
	item: ProjectRowItem;
	/** Folds or unfolds a folding row, or reads a more row's next page. */
	onPress: () => void;
	/** A project row's menu; undefined while it offers none. */
	onLongPress?: () => void;
	/** A change to this project is in the organization journal. */
	changing?: boolean;
}

/** A project's name as the Board shows it. */
export const projectName = (project: NavigationProjectSummary) =>
	project.name || project.working_dir || "Untitled project";

/** Where a row's content starts: the 16pt margin, then 16pt per level. */
export const projectIndent = (depth: number) => 16 + 16 * depth;

export function ProjectTreeRow({ item, onPress, onLongPress, changing = false }: ProjectTreeRowProps): ReactElement {
	const { palette } = useColors();
	const scale = useTextScale();
	const text = (content: string, style: Record<string, unknown>, flex = false) => (
		<Text
			allowFontScaling={Platform.OS !== "ios"}
			numberOfLines={1}
			style={{ ...(flex ? { flex: 1, minWidth: 0 } : {}), ...style }}
		>
			{content}
		</Text>
	);
	const offline = text("Offline", { fontSize: 13 * scale, fontWeight: "600", color: palette.attentionInk });
	const liveCount = (count: number | null) =>
		count === null
			? null
			: text(`${count} live`, { fontSize: 13 * scale, color: palette.inkLow, fontVariant: ["tabular-nums"] });
	const folding = (
		folded: boolean,
		height: number,
		label: string,
		children: ReactNode,
		extra: { testID?: string; onLongPress?: () => void } = {},
	) => (
		<Pressable
			testID={extra.testID}
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ expanded: !folded }}
			onPress={onPress}
			onLongPress={extra.onLongPress}
			style={({ pressed }) => ({
				minHeight: height,
				paddingLeft: projectIndent(item.depth),
				paddingRight: 16,
				flexDirection: "row",
				alignItems: "center",
				columnGap: 8,
				opacity: changing ? 0.5 : 1,
				backgroundColor: pressed ? palette.pressed : palette.page,
			})}
		>
			{children}
			<SymbolView name={folded ? "chevron.right" : "chevron.down"} size={13 * scale} tintColor={palette.inkLow} />
		</Pressable>
	);

	switch (item.kind) {
		case "host": {
			const { host } = item;
			return folding(
				item.folded,
				48,
				[host.label, host.online ? item.liveCount !== null && `${item.liveCount} live` : "Offline"]
					.filter(Boolean)
					.join(", "),
				<>
					<SymbolView name="server.rack" size={17 * scale} tintColor={palette.inkMid} />
					{text(
						host.label,
						{ fontSize: 17 * scale, lineHeight: 22 * scale, fontWeight: "600", color: palette.inkHi },
						true,
					)}
					{host.online ? liveCount(item.liveCount) : offline}
				</>,
			);
		}
		case "project": {
			const { project } = item;
			const name = projectName(project);
			return folding(
				item.folded,
				48,
				[name, project.favorite && "pinned", item.liveCount !== null && `${item.liveCount} live`]
					.filter(Boolean)
					.join(", "),
				<>
					<SymbolView name="folder" size={17 * scale} tintColor={palette.inkMid} />
					{project.favorite ? <SymbolView name="pin.fill" size={12 * scale} tintColor={palette.inkLow} /> : null}
					{text(name, { fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkHi }, true)}
					{liveCount(item.liveCount)}
				</>,
				{ testID: "project-row", onLongPress },
			);
		}
		case "branch": {
			const { host } = item;
			return folding(
				item.folded,
				44,
				host.online ? host.label : `${host.label}, Offline`,
				<>
					<SymbolView name="server.rack" size={15 * scale} tintColor={palette.inkMid} />
					{text(host.label, { fontSize: 15 * scale, fontWeight: "600", color: palette.inkMid }, true)}
					{host.online ? null : offline}
				</>,
			);
		}
		case "archivedGroup":
			return folding(
				item.folded,
				44,
				item.count === null ? "Archived" : `Archived, ${plural(item.count, "session")}`,
				text(item.count === null ? "Archived" : `Archived · ${item.count}`, { fontSize: 15 * scale, color: palette.inkMid }, true),
			);
		case "tier":
			return (
				<View style={{ minHeight: 32, paddingLeft: projectIndent(item.depth), paddingRight: 16, justifyContent: "center" }}>
					{text(item.label, { fontSize: 13 * scale, fontWeight: "600", color: palette.inkMid })}
				</View>
			);
		case "more":
		case "moreProjects": {
			const more =
				item.kind === "more" ? `${item.remaining} more` : `${item.remaining} more ${item.remaining === 1 ? "project" : "projects"}`;
			return (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={more}
					onPress={onPress}
					style={({ pressed }) => ({
						minHeight: 44,
						paddingLeft: projectIndent(item.depth),
						paddingRight: 16,
						justifyContent: "center",
						backgroundColor: pressed ? palette.pressed : palette.page,
					})}
				>
					{text(more, { fontSize: 13 * scale, color: palette.inkLow })}
				</Pressable>
			);
		}
		case "loading":
			return (
				<View
					style={{ paddingLeft: projectIndent(item.depth), paddingRight: 16, paddingVertical: 4 }}
					accessibilityLabel="Loading sessions"
				>
					<View testID="project-loading" style={{ height: 48, borderRadius: 10, backgroundColor: palette.inset }} />
				</View>
			);
		case "failed":
			return (
				<View style={{ minHeight: 44, paddingLeft: projectIndent(item.depth), paddingRight: 16, justifyContent: "center" }}>
					{text("Couldn't load these sessions.", { fontSize: 13 * scale, color: palette.inkMid })}
				</View>
			);
	}
}

const ORGANIZE_BY_LABELS: Record<OrganizeBy, string> = {
	"project-host": "Project, then host",
	"host-project": "Host, then project",
};

/** A project section's header (spec 7.1), in the band headers' style with a
 * fold chevron. The Projects header carries Organize by at its trailing edge
 * once the hub has a second host; pressing it chooses the other order. */
export function ProjectSectionHeader({
	title,
	label,
	folded,
	onToggle,
	organize,
}: {
	title: string;
	label: string;
	folded: boolean;
	onToggle: () => void;
	organize: { by: OrganizeBy; onChange: (next: OrganizeBy) => void } | null;
}): ReactElement {
	const { palette } = useColors();
	const scale = useTextScale();
	const other: OrganizeBy = organize?.by === "project-host" ? "host-project" : "project-host";
	return (
		<View style={{ flexDirection: "row", alignItems: "center" }}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={label}
				accessibilityState={{ expanded: !folded }}
				onPress={onToggle}
				style={({ pressed }) => ({
					flex: 1,
					minHeight: 44,
					paddingLeft: 16,
					paddingRight: organize ? 8 : 16,
					flexDirection: "row",
					alignItems: "center",
					columnGap: 8,
					backgroundColor: pressed ? palette.pressed : palette.page,
				})}
			>
				<Text
					testID="project-section-header"
					allowFontScaling={Platform.OS !== "ios"}
					numberOfLines={1}
					style={{ ...bandHeaderText(palette, scale), flexShrink: 1 }}
				>
					{title}
				</Text>
				<View style={{ flex: 1, alignItems: "flex-end" }}>
					<FoldChevron folded={folded} />
				</View>
			</Pressable>
			{organize ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={`Organize by: ${ORGANIZE_BY_LABELS[organize.by]}`}
					accessibilityHint={`Changes to ${ORGANIZE_BY_LABELS[other]}`}
					onPress={() => organize.onChange(other)}
					style={({ pressed }) => ({
						minHeight: 44,
						paddingHorizontal: 16,
						flexDirection: "row",
						alignItems: "center",
						columnGap: 4,
						opacity: pressed ? 0.6 : 1,
					})}
				>
					<SymbolView name="arrow.left.arrow.right" size={13 * scale} tintColor={palette.accentInk} />
					<Text allowFontScaling={Platform.OS !== "ios"} style={{ fontSize: 13 * scale, color: palette.accentInk }}>
						{ORGANIZE_BY_LABELS[organize.by]}
					</Text>
				</Pressable>
			) : null}
		</View>
	);
}
