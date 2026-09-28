// A subagent's screen is its own session (spec 9, ruling 30): the Session for
// the subagent's ref, with its coordinator named so the Session can hold the
// subagent bar where the composer would be while the hub takes no message.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useMemo } from "react";
import { ConversationScreen, type Routes } from "../screens";

export function SubagentScreen({ route, navigation }: NativeStackScreenProps<Routes, "Subagent">) {
	const { hubId, ref, title, coordinator } = route.params;
	// The Session's own params; the route keeps its key, so the Session asks
	// whether this screen is in front.
	const session = useMemo(
		() => ({ key: route.key, name: "Conversation" as const, params: { hubId, ref, title } }),
		[route.key, hubId, ref, title],
	);
	return <ConversationScreen route={session} navigation={navigation as never} subagentOf={coordinator} />;
}
