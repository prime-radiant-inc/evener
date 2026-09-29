// New session's project picker (spec 11): the chosen host's recent projects,
// by name with the path beneath in Menlo, filtered by a search field, then a
// way to browse that host's folders.
import { parentOf } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useState } from "react";
import { View } from "react-native";
import { useStore } from "zustand";
import { Group, GroupedPage, GroupFooter, Row } from "../sheet/Grouped";
import { SearchField } from "../sheet/SearchField";
import { SheetStatus } from "../sheet/SheetStatus";
import { projectName } from "./launchSetup";
import { type NewSessionRoutes, useNewSession } from "./newSessionContext";

export function ProjectPicker({ navigation }: NativeStackScreenProps<NewSessionRoutes, "Project">) {
	const { store, hostLabel } = useNewSession();
	const { source, cwd, projects, metadataError, setCwd } = useStore(store);
	const [query, setQuery] = useState("");
	const host = hostLabel(source);
	const chosen = cwd.trim();
	const search = query.trim().toLowerCase();
	const shown = projects.filter((path) => path.toLowerCase().includes(search));
	return (
		<GroupedPage>
			<SheetStatus />
			<View style={{ paddingHorizontal: 16, paddingTop: 8 }}>
				<SearchField label={`Projects on ${host}`} value={query} onChangeText={setQuery} />
			</View>
			{metadataError ? <GroupFooter tone="danger">{metadataError}</GroupFooter> : null}
			{shown.length > 0 ? (
				<>
					<Group label={`Recent on ${host}`}>
						{shown.map((path) => (
							<Row
								key={path}
								label={projectName(path)}
								sub={path}
								machineSub
								checked={path === chosen}
								onPress={() => {
									void setCwd(path, true);
									navigation.goBack();
								}}
							/>
						))}
					</Group>
				</>
			) : null}
			<Group>
				<Row
					label={`Browse folders on ${host}…`}
					icon="folder"
					tone="accent"
					onPress={() => navigation.navigate("Browse", { dir: chosen ? parentOf(chosen) : "" })}
				/>
			</Group>
		</GroupedPage>
	);
}
