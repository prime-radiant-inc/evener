import type { NavigationContainerRef } from "@react-navigation/native";
import type { Notice } from "../board/notices";
import { workspaceSessionPath } from "../location";
import type { Routes } from "../screens";
import type { BannerTarget } from "./alertCenter";

export type OpenAlertTarget = (
	navigation: NavigationContainerRef<Routes>,
	hubId: string,
	target: Exclude<BannerTarget, { kind: "notice" }> | { kind: "notice"; notice: Notice },
) => void;

// Notices are derived by the selected hub. Its Hosts page owns the offline
// host's registry entry, and its credentials and plugin inventory are local.
const noticeSection: Record<Notice["kind"], string> = {
	host: "hosts",
	signIn: "credentials",
	plugin: "plugins-manager",
};

export const openAlertTarget: OpenAlertTarget = (navigation, hubId, target) => {
	let path: string;
	switch (target.kind) {
		case "newSession":
			path = "/new";
			break;
		case "session":
			path = workspaceSessionPath(target.ref);
			break;
		case "needsYou":
			path = "/?attention=needs-you";
			break;
		case "notice":
			path = `/settings/${noticeSection[target.notice.kind]}?host=local`;
			break;
	}
	navigation.navigate("Workspace", { hubId, path }, { pop: true });
};
