// Hubs (spec 12): the phone's saved hubs, the selected one with its connection
// state, and adding one. Choosing another hub selects it; the sheet then closes
// onto that hub's Board by itself (useClosesOnHubChange), so this page never
// navigates for it.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { SymbolView } from "expo-symbols";
import { Pressable, View } from "react-native";
import { useConnectionStatusText } from "../board/connectionStatus";
import { useConnection } from "../ConnectionProvider";
import { Group, GroupedPage, GroupFooter, Row } from "../sheet/Grouped";
import { useColors } from "../ui";
import { hubConnectionWord } from "./hubHeader";
import { type HubRoutes, useHubSheet } from "./hubSheetContext";

export function HubsPage({ navigation }: NativeStackScreenProps<HubRoutes, "Hubs">) {
	const { profiles, activeProfile, selectHub } = useConnection();
	const { palette } = useColors();
	const { ready } = useHubSheet();
	const connectionLine = useConnectionStatusText();
	return (
		<GroupedPage>
			<Group label="Hubs">
				{profiles.map((profile) => {
					const selected = profile.id === activeProfile?.id;
					// Only the selected hub has a connection to describe; the others are
					// simply not in use (prototype hub.js:202).
					const sub = selected ? `${profile.origin} · ${hubConnectionWord(ready, connectionLine)}` : profile.origin;
					return (
						// The details button sits beside the row rather than inside it,
						// so VoiceOver reaches it as its own element.
						<View key={profile.id} style={{ flexDirection: "row", alignItems: "center" }}>
							<View style={{ flex: 1 }}>
								<Row
									label={profile.name}
									sub={sub}
									machineSub
									checked={selected}
									onPress={selected ? undefined : () => selectHub(profile.id)}
								/>
							</View>
							<Pressable
								accessibilityRole="button"
								accessibilityLabel={`Details for ${profile.name}`}
								onPress={() => navigation.navigate("HubDetails", { id: profile.id })}
								style={{ width: 44, height: 44, alignItems: "center", justifyContent: "center", marginRight: 4 }}
							>
								<SymbolView name="info.circle" tintColor={palette.accentInk} size={20} />
							</Pressable>
						</View>
					);
				})}
			</Group>
			<Group label="Add a hub">
				<Row
					icon="qrcode.viewfinder"
					label="Scan pairing code"
					chevron
					onPress={() => navigation.navigate("AddHub", { how: "scan" })}
				/>
				<Row
					icon="doc.on.clipboard"
					label="Paste pairing link"
					chevron
					onPress={() => navigation.navigate("AddHub", { how: "paste" })}
				/>
				<Row
					icon="keyboard"
					label="Enter the address"
					chevron
					onPress={() => navigation.navigate("AddHub", { how: "address" })}
				/>
			</Group>
			<GroupFooter>In Evener on your computer, open Settings, then Mobile app, to show a pairing code.</GroupFooter>
		</GroupedPage>
	);
}
