// Where the banner lives (spec 13.3): over every screen, just below the nav
// bar so it never covers Back, the title or the ask dock (usability round 1,
// problem 3), inside the navigation container so a tap can open what it
// names.
import { getDefaultHeaderHeight } from "@react-navigation/elements";
import type { NavigationContainerRef } from "@react-navigation/native";
import { Alert, View } from "react-native";
import { useSafeAreaFrame, useSafeAreaInsets } from "react-native-safe-area-context";
import { hasHub } from "../connection";
import { useConnection } from "../ConnectionProvider";
import type { Routes } from "../screens";
import { AlertBanner } from "./AlertBanner";
import { useAlertCenter, useAlertSnapshot, useNoticeFor } from "./alertsContext";
import { openAlertTarget } from "./openAlertTarget";

export function AlertBannerHost({ navigation }: { navigation: NavigationContainerRef<Routes> }) {
	const center = useAlertCenter();
	const { banner } = useAlertSnapshot();
	const noticeFor = useNoticeFor();
	const { activeProfile, profiles, selectHub } = useConnection();
	const frame = useSafeAreaFrame();
	const insets = useSafeAreaInsets();
	const top = getDefaultHeaderHeight(frame, false, insets.top) + 4;

	const open = () => {
		const target = center.tap();
		if (target === null) return;
		// A failed start belongs to the hub where it was submitted. All other
		// alerts belong to the active hub and are cleared on a switch.
		const hubId = target.kind === "newSession" ? target.hubId : activeProfile?.id;
		if (hubId === undefined) return;
		if (target.kind === "newSession") {
			if (!hasHub(profiles, target.hubId)) {
				Alert.alert(
					`${target.hubName} was removed`,
					"Its New session draft was removed with it, so there's nothing to open.",
				);
				return;
			}
			if (target.hubId !== activeProfile?.id) selectHub(target.hubId);
		}
		if (target.kind === "notice") {
			// A notice that resolved since the banner dropped in opens nothing.
			const notice = noticeFor(target.key);
			if (notice) openAlertTarget(navigation, hubId, { kind: "notice", notice });
		} else openAlertTarget(navigation, hubId, target);
	};

	return (
		<View pointerEvents="box-none" style={{ position: "absolute", left: insets.left, right: insets.right, top }}>
			{banner ? (
				<AlertBanner
					key={banner.id}
					banner={banner}
					onTap={open}
					onDismiss={() => center.dismiss()}
					onTouch={(down) => center.touch(down)}
				/>
			) : null}
		</View>
	);
}
