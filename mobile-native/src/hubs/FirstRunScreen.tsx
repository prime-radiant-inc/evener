// First run (spec 15, ruling 23): the root Hubs route. It asks to connect to a
// hub by scanning its pairing code or pasting its pairing link, with typing the
// address as a quieter third way, and shows the chosen way in place. Once hubs
// are saved it lists them too: after the selected hub is removed, and when the
// Board is swiped back to this screen.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useState } from "react";
import { Text, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useConnection } from "../ConnectionProvider";
import type { Routes } from "../screens";
import { Group, GroupedPage, Row } from "../sheet/Grouped";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { AddHub, type How } from "./AddHub";

export function FirstRunScreen({ navigation }: NativeStackScreenProps<Routes, "Hubs">) {
	const { profiles, selectHub } = useConnection();
	const { palette } = useColors();
	const scale = useTextScale();
	const [how, setHow] = useState<How | null>(null);

	function openBoard() {
		setHow(null);
		navigation.navigate("Sessions");
	}

	if (how)
		return (
			<SafeAreaView style={{ flex: 1, backgroundColor: palette.canvas }}>
				<Group>
					<Row label="Back" tone="accent" onPress={() => setHow(null)} />
				</Group>
				<AddHub how={how} onConnected={openBoard} />
			</SafeAreaView>
		);

	return (
		<SafeAreaView style={{ flex: 1, backgroundColor: palette.canvas }}>
			<GroupedPage>
				<View style={{ paddingHorizontal: 32, paddingTop: 48, paddingBottom: 12, gap: 8 }}>
					<Text
						accessibilityRole="header"
						allowFontScaling={allowFontScaling}
						style={{ color: palette.inkHi, fontSize: 28 * scale, lineHeight: 34 * scale, fontWeight: "600" }}
					>
						Connect to your hub
					</Text>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ color: palette.inkMid, fontSize: 17 * scale, lineHeight: 22 * scale }}
					>
						In Evener on your computer, open Settings, then Mobile app.
					</Text>
				</View>
				<Group>
					<Row icon="qrcode.viewfinder" label="Scan pairing code" chevron onPress={() => setHow("scan")} />
					<Row icon="doc.on.clipboard" label="Paste pairing link" chevron onPress={() => setHow("paste")} />
				</Group>
				<Group>
					<Row icon="keyboard" label="Enter the address" chevron onPress={() => setHow("address")} />
				</Group>
				{profiles.length > 0 ? (
					<>
						<Group label="Saved hubs">
							{profiles.map((profile) => (
								<Row
									key={profile.id}
									label={profile.name}
									sub={profile.origin}
									machineSub
									chevron
									onPress={() => {
										selectHub(profile.id);
										openBoard();
									}}
								/>
							))}
						</Group>
					</>
				) : null}
			</GroupedPage>
		</SafeAreaView>
	);
}
