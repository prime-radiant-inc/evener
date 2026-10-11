// One read about the chosen host and project for New session (the hub's launch
// defaults, the project's branch): asked while the sheet is ready, again
// whenever the host, the project or `revision` changes, with an answer for a
// place the form has left dropped. Null before the answer and after a failure;
// a place's last answer stays while it is asked again.
import type { AnyNotification } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import type { ConversationClientLike, NewSessionService } from "../../../mobile/src/services/newSession";
import { creationService } from "./creations";

/** `read` must be a stable function (declared at module level), or the page
 * asks again on every render. */
export function useHostRead<T>(
	client: ConversationClientLike | null,
	ready: boolean,
	host: string,
	cwd: string,
	read: (service: NewSessionService, host: string, cwd: string) => Promise<T>,
	revision = 0,
): T | null {
	const project = cwd.trim();
	const place = JSON.stringify([host, project]);
	const [known, setKnown] = useState<{ place: string; value: T | null } | null>(null);
	useEffect(() => {
		// The revision re-runs this read; it isn't read inside.
		void revision;
		if (!client || !ready || !project) return;
		let current = true;
		read(creationService(client), host, project).then(
			(value) => current && setKnown({ place, value }),
			() => current && setKnown({ place, value: null }),
		);
		return () => {
			current = false;
		};
	}, [client, ready, host, project, place, read, revision]);
	return known?.place === place ? known.value : null;
}

/** A count that goes up whenever the hub sends one of `methods`, for reads
 * that must be asked again then. `methods` must be a stable array. */
export function useHubRevision(
	client: Pick<ConversationClientLike, "onNotification"> | null,
	methods: readonly string[],
): number {
	const [revision, setRevision] = useState(0);
	useEffect(
		() =>
			client?.onNotification((notification: AnyNotification) => {
				if (methods.includes(notification.method)) setRevision((count) => count + 1);
			}),
		[client, methods],
	);
	return revision;
}
