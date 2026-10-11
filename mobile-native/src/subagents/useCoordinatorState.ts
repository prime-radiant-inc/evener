// A subagent's coordinator, read by a screen above the subagent (its bar,
// the stop sheet) without taking the connection's subscription, which the
// transcript underneath follows. Read once per connection; a new connection,
// or none, forgets the last answer, and a read from a connection the screen
// has left never lands, even when it answers late.
import { useEffect, useState } from "react";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { SessionLink, type SessionState } from "../session/sessionMessage";

/** The coordinator's state; "unreadable" when the read failed; null until a
 * read answers, or with no connection. */
export function useCoordinatorState(
	client: ConversationClientLike | null,
	coordinatorRef: string,
): SessionState | "unreadable" | null {
	const [state, setState] = useState<SessionState | "unreadable" | null>(null);
	useEffect(() => {
		setState(null);
		if (!client) return;
		let current = true;
		const link = new SessionLink(client, coordinatorRef);
		link.read({ follow: false }).then(
			(session) => {
				if (current) setState(session);
			},
			() => {
				if (current) setState("unreadable");
			},
		);
		return () => {
			current = false;
			link.dispose();
		};
	}, [client, coordinatorRef]);
	return state;
}
