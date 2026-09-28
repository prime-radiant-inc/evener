// The Session's ⋯ menu (spec 8.1 and 8.7), as the native header items iOS
// draws as a UIMenu. It is data: the screen decides what each choice does.
// Subagents, Tasks, Notes & links, Ask aside and Shut down appear only when
// they can act. Delete lives in the Session sheet.
import type { ContentLevel } from "@evener/appwire-client";
import type {
	NativeStackHeaderItem,
	NativeStackHeaderItemMenuAction,
	NativeStackHeaderItemMenuSubmenu,
} from "@react-navigation/native-stack";
import { DETAIL_LEVELS, detailMenuLabel } from "./detailLevels";

export type SessionMenuAction =
	| { kind: "level"; level: ContentLevel }
	| { kind: "find" | "subagents" | "tasks" | "notes" | "info" | "aside" | "pin" | "archive" | "shutDown" };

export interface SessionMenuInput {
	current: ContentLevel | "custom" | null;
	hasSubagents: boolean;
	connected: boolean;
	/** The session keeps shared notes (`capabilities.sharedNotes`). They read
	 * without a connection, so this needs none. */
	sharedNotes: boolean;
	canAside: boolean;
	canShutDown: boolean;
	choose(action: SessionMenuAction): void;
}

type Entry = NativeStackHeaderItemMenuAction | NativeStackHeaderItemMenuSubmenu;

export function sessionMenu(input: SessionMenuInput): NativeStackHeaderItem[] {
	const { choose } = input;
	const item = (
		label: string,
		kind: Exclude<SessionMenuAction["kind"], "level">,
		extra: Partial<NativeStackHeaderItemMenuAction> = {},
	): NativeStackHeaderItemMenuAction => ({ type: "action", label, onPress: () => choose({ kind }), ...extra });
	const levels: NativeStackHeaderItemMenuSubmenu = {
		type: "submenu",
		label: detailMenuLabel(input.current),
		items: [
			{
				type: "submenu",
				inline: true,
				label: "How much of the agent's work this session shows",
				items: DETAIL_LEVELS.map(({ level, label, description }) => ({
					type: "action",
					label,
					description,
					state: level === input.current ? "on" : "off",
					onPress: () => choose({ kind: "level", level }),
				})),
			},
		],
	};
	const entries: Entry[] = [
		levels,
		item("Find in session", "find"),
		...(input.hasSubagents ? [item("Subagents", "subagents")] : []),
		...(input.connected ? [item("Tasks", "tasks")] : []),
		...(input.sharedNotes ? [item("Notes & links", "notes")] : []),
		item("Session info", "info"),
		...(input.canAside
			? [item("Ask aside…", "aside", { description: "A side question in its own session; this one keeps working" })]
			: []),
		item("Pin to category…", "pin"),
		item("Archive", "archive"),
		...(input.canShutDown ? [item("Shut down", "shutDown", { destructive: true })] : []),
	];
	return [
		{
			type: "menu",
			label: "Session actions",
			accessibilityLabel: "Session actions",
			icon: { type: "sfSymbol", name: "ellipsis.circle" },
			menu: { items: entries },
		},
	];
}
