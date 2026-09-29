// Where the banner lives (spec 13.3): over every screen, just below the nav
// bar so it never covers Back, the title or the ask dock (usability round 1,
// problem 3), inside the navigation container so a tap can open what it
// names.
import { getDefaultHeaderHeight } from "@react-navigation/elements";
import { type NavigationContainerRef, StackActions } from "@react-navigation/native";
import { Alert, View } from "react-native";
import { useSafeAreaFrame, useSafeAreaInsets } from "react-native-safe-area-context";
import { openNotice } from "../board/BoardNotices";
import { requestBoardJump } from "../board/boardJump";
import { hasHub } from "../connection";
import { useConnection } from "../ConnectionProvider";
import type { Routes } from "../screens";
import { AlertBanner } from "./AlertBanner";
import { useAlertCenter, useAlertSnapshot, useNoticeFor } from "./alertsContext";

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
		if (target.kind === "newSession") {
			// A failed start names its own hub, and survives a hub switch or a
			// disconnect, so it opens whichever hub is selected, or none. Its draft
			// waits in that hub's creation store (#3104); a hub removed since took
			// its draft with it, so the tap says so rather than doing nothing. A
			// start on another hub opens once that hub is selected, so its sheet has
			// its own connection.
			if (!hasHub(profiles, target.hubId)) {
				Alert.alert(
					`${target.hubName} was removed`,
					"Its New session draft was removed with it, so there's nothing to open.",
				);
				return;
			}
			if (target.hubId !== activeProfile?.id) selectHub(target.hubId);
			navigation.dispatch(StackActions.push("NewSession", { hubId: target.hubId, hubName: target.hubName }));
			return;
		}
		// The center is reset for every hub, so what else it names is the
		// selected hub's.
		const hubId = activeProfile?.id;
		if (hubId === undefined) return;
		if (target.kind === "session")
			// Pushed, so Back returns to where you were (spec 6).
			navigation.dispatch(StackActions.push("Conversation", { hubId, ref: target.ref, title: target.title }));
		else if (target.kind === "needsYou") {
			navigation.dispatch(StackActions.popTo("Sessions"));
			requestBoardJump("needsYou");
		} else {
			// A notice that resolved since the banner dropped in opens nothing.
			const notice = noticeFor(target.key);
			if (notice) openNotice(navigation, hubId, notice);
		}
	};

	return (
		<View pointerEvents="box-none" style={{ position: "absolute", left: 0, right: 0, top }}>
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
