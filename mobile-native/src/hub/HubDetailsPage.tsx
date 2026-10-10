// A saved hub's details (spec 12's Hubs): its name, its address and removing
// it. Removing the selected hub leaves the navigation to the sheet, which
// leaves for the first-run screen once no hub is selected (Review Focus 5).
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useLayoutEffect, useState } from "react";
import { Alert } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { destructiveButton } from "../haptics";
import { HubEditor } from "../HubEditor";
import { Group, GroupedPage, GroupFooter, GroupGap, Row } from "../sheet/Grouped";
import type { HubRoutes } from "./hubSheetContext";

export function HubDetailsPage({ navigation, route }: NativeStackScreenProps<HubRoutes, "HubDetails">) {
	const { profiles, activeProfile, updateHub, removeHub } = useConnection();
	const profile = profiles.find((candidate) => candidate.id === route.params.id);
	const [editing, setEditing] = useState(false);
	const [failure, setFailure] = useState<string | null>(null);
	const name = profile?.name;

	useLayoutEffect(() => {
		if (name !== undefined) navigation.setOptions({ title: name });
	}, [navigation, name]);

	// A removal can drop the hub and still fail on its local data
	// (removeSavedHub): the page's hub is gone, but the reason stays on screen.
	if (!profile)
		return failure ? (
			<GroupedPage>
				<GroupGap />
				<GroupFooter tone="danger">{failure}</GroupFooter>
			</GroupedPage>
		) : null;

	async function remove(id: string, label: string) {
		const selected = id === activeProfile?.id;
		setFailure(null);
		try {
			await removeHub(id);
		} catch (error) {
			const reason = error instanceof Error ? error.message : "Couldn't remove this hub.";
			// Removing the selected hub deselects it before a cleanup error
			// comes back, and the sheet leaves for first run with this page:
			// only an alert outlives that.
			if (selected) Alert.alert(`Couldn't finish removing ${label}`, reason);
			else setFailure(reason);
			return;
		}
		if (!selected) navigation.goBack();
	}

	function confirmRemove(id: string, label: string) {
		Alert.alert(`Remove ${label}?`, "The saved hub, its token and its drafts are removed from this phone.", [
			{ text: "Cancel", style: "cancel" },
			destructiveButton("Remove", () => remove(id, label)),
		]);
	}

	return (
		<GroupedPage>
			{editing ? <HubEditor profile={profile} save={updateHub} close={() => setEditing(false)} /> : null}
			<Group>
				<Row label="Name" value={profile.name} chevron onPress={() => setEditing(true)} />
				<Row label="Address" sub={profile.origin} machineSub />
			</Group>
			<Group>
				<Row label="Remove this hub" tone="danger" onPress={() => confirmRemove(profile.id, profile.name)} />
			</Group>
			{failure ? <GroupFooter tone="danger">{failure}</GroupFooter> : null}
		</GroupedPage>
	);
}
