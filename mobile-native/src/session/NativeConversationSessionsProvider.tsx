// The app's one native conversation binding registry, constructed under the
// preferences provider and above the navigator. It feeds the selected hub's
// ready client and matching transcript preferences into the registry, and
// gives every committed conversation view the same attachment hook. A view
// that never commits an attachment creates no store, host, SQLite handle or
// subscription.
import { createContext, type ReactNode, useContext, useEffect, useLayoutEffect, useState } from "react";
import { useConnection } from "../ConnectionProvider";
import { useNativePreferences } from "../NativePreferencesProvider";
import { NativeConversationSessions, type NativeConversationAttachment } from "./nativeConversationSessions";

const Context = createContext<NativeConversationSessions | null>(null);

export function NativeConversationSessionsProvider({ children }: { children: ReactNode }) {
	const { activeProfile, client, state } = useConnection();
	const preferences = useNativePreferences();
	const hubId = activeProfile?.id ?? null;
	const connectedClient = state === "ready" && client !== null ? client : null;
	// Exactly one registry for this native client tree, not one per route.
	const [sessions] = useState(() => new NativeConversationSessions());
	useEffect(() => {
		sessions.setConnection(hubId, connectedClient);
	}, [sessions, hubId, connectedClient]);
	// The hub's transcript display preference reaches only entries on that hub.
	const displayHubId = preferences.hubId === hubId ? hubId : null;
	const displayConfig = displayHubId === null ? null : preferences.config;
	useEffect(() => {
		sessions.setHubDisplayConfig(displayHubId, displayConfig);
	}, [sessions, displayHubId, displayConfig]);
	useEffect(() => () => sessions.dispose(), [sessions]);
	return <Context.Provider value={sessions}>{children}</Context.Provider>;
}

export function useNativeConversationSessions(): NativeConversationSessions {
	const value = useContext(Context);
	if (value === null) throw new Error("NativeConversationSessionsProvider is required.");
	return value;
}

/** Acquire in a committed layout effect, never in render/memo/getSnapshot.
 * Abandoned renders create no entry. Strict Mode's setup/cleanup/setup retires
 * the first attachment and commits a fresh one, with no leaked refcount. */
export function useNativeConversationAttachment(hubId: string, ref: string): NativeConversationAttachment | null {
	const sessions = useNativeConversationSessions();
	// The committed handle AND the key it belongs to. A key-change render still
	// has the previous render's state until its own layout effect runs; handing
	// that handle out would let the new session's view act against the old
	// entry. Only an exact key match is a live attachment for this caller.
	const [committed, setCommitted] = useState<{
		hubId: string;
		ref: string;
		handle: NativeConversationAttachment;
	} | null>(null);
	useLayoutEffect(() => {
		const handle = sessions.attach(hubId, ref);
		setCommitted({ hubId, ref, handle });
		return () => {
			handle.release();
		};
	}, [sessions, hubId, ref]);
	return committed !== null && committed.hubId === hubId && committed.ref === ref ? committed.handle : null;
}
