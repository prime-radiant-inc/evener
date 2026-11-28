import { StackActions } from "@react-navigation/native";
import { openNotice } from "../board/BoardNotices";
import { requestBoardJump } from "../board/boardJump";
import type { OpenAlertTarget } from "./openAlertTarget";

export const openAlertTarget: OpenAlertTarget = (navigation, hubId, target) => {
	switch (target.kind) {
		case "newSession":
			navigation.dispatch(StackActions.push("NewSession", { hubId, hubName: target.hubName }));
			break;
		case "session":
			navigation.dispatch(StackActions.push("Conversation", { hubId, ref: target.ref, title: target.title }));
			break;
		case "needsYou":
			navigation.dispatch(StackActions.popTo("Sessions"));
			requestBoardJump("needsYou");
			break;
		case "notice":
			openNotice(navigation, hubId, target.notice);
			break;
	}
};
