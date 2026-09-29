// Browsing the chosen host's folders for New session's project (spec 11): one
// page whose folder changes in place, so Back leaves the browser at once. The
// listing comes from that host, through the hub (ruling 2). "Use this folder"
// sets the project; "New folder" makes one there and moves into it.
import { buildPathRows, childrenPrefix, friendlyErrorMessage } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useEffect, useMemo, useState } from "react";
import { Alert, Platform } from "react-native";
import { useStore } from "zustand";
import { creationService } from "./creations";
import { useOptionalSnapshot } from "../hosts/useHubFleet";
import { HubPaths } from "../hubPaths";
import { Group, GroupedPage, GroupFooter, GroupLabel, Row } from "../sheet/Grouped";
import { SheetStatus } from "../sheet/SheetStatus";
import { type NewSessionRoutes, useNewSession } from "./newSessionContext";

export function BrowseFolders({ navigation, route }: NativeStackScreenProps<NewSessionRoutes, "Browse">) {
	const { store, client, ready, hostLabel } = useNewSession();
	const { source, cwd, setCwd } = useStore(store);
	const [dir, setDir] = useState(route.params.dir);
	const [createError, setCreateError] = useState<string | null>(null);
	const paths = useMemo(() => (client ? new HubPaths(client, false, source) : null), [client, source]);
	useEffect(() => () => paths?.dispose(), [paths]);
	useEffect(() => {
		void paths?.load(childrenPrefix(dir));
	}, [paths, dir]);
	const listing = useOptionalSnapshot(paths);
	const rows = buildPathRows({
		kind: "dir",
		currentDir: dir,
		entries: listing?.error ? [] : (listing?.paths ?? null),
		value: cwd,
		recents: [],
		showRecents: false,
		// HubPaths' own sentence offers a retry and manual entry, which this page
		// has neither of; moving to another folder reads again.
		listError: listing?.error
			? `Couldn't open this folder on ${hostLabel(source)}. Go up a folder or choose another.`
			: null,
	});
	const open = (next: string) => {
		setCreateError(null);
		setDir(next);
	};
	const create = async (name: string) => {
		if (!client || !name.trim()) return;
		try {
			// The hub makes only an absolute path, or one under "~/" (hubDirsCreate,
			// cmd/evener-hub/app_dirs.go); home is the one folder the phone can't
			// name, so a folder made there goes under "~/".
			const parent = dir ? childrenPrefix(dir) : "~/";
			const made = await creationService(client).createDirectory(source, parent + name.trim());
			open(made);
		} catch (error) {
			setCreateError(friendlyErrorMessage(error));
		}
	};
	const label = rows.find((row) => row.kind === "group");
	const folders = rows.filter((row) => row.kind === "parent" || row.kind === "dir");
	const status = rows.find((row) => row.kind === "status");
	return (
		<GroupedPage>
			<SheetStatus />
			{label && folders.length === 0 ? <GroupLabel machine>{label.label}</GroupLabel> : null}
			{folders.length > 0 ? (
				<Group label={label?.label} machineLabel>
					{folders.map((row) =>
						row.kind === "parent" ? (
							<Row key={row.key} label="Up one folder" icon="arrow.up" onPress={() => open(row.path)} />
						) : row.kind === "dir" ? (
							<Row key={row.key} label={row.name} icon="folder" chevron onPress={() => open(row.path)} />
						) : null,
					)}
				</Group>
			) : null}
			{status ? <GroupFooter>{status.text}</GroupFooter> : null}
			<Group>
				{dir ? (
					<Row
						key="use"
						label="Use this folder"
						tone="accent"
						onPress={() => {
							void setCwd(dir, true);
							navigation.popTo("Form");
						}}
					/>
				) : null}
				{/* Alert.prompt is iOS-only, so New folder stays off other platforms. */}
				{Platform.OS === "ios" ? (
					<Row
						key="new"
						label="New folder"
						icon="folder.badge.plus"
						tone="accent"
						disabled={!ready || !client}
						onPress={() => Alert.prompt("New folder", `In ${dir || "your home folder"}`, (name) => void create(name))}
					/>
				) : null}
			</Group>
			{createError ? <GroupFooter tone="danger">{createError}</GroupFooter> : null}
		</GroupedPage>
	);
}
