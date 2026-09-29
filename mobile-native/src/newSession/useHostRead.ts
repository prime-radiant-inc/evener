// One read about the chosen host and project for a New session page (the
// hub's launch defaults, the project's branch): asked while the sheet is
// ready, again whenever the host or project changes, with an answer for a
// place the form has left dropped. Null before the answer and after a failure.
import { useEffect, useState } from "react";
import { createNewSessionService, type NewSessionService } from "../../../mobile/src/services/newSession";
import { useNewSession } from "./newSessionContext";

/** `read` must be a stable function (declared at module level), or the page
 * asks again on every render. */
export function useHostRead<T>(
	host: string,
	cwd: string,
	read: (service: NewSessionService, host: string, cwd: string) => Promise<T>,
): T | null {
	const { client, ready } = useNewSession();
	const project = cwd.trim();
	const place = JSON.stringify([host, project]);
	const [known, setKnown] = useState<{ place: string; value: T | null } | null>(null);
	useEffect(() => {
		if (!client || !ready || !project) return;
		let current = true;
		read(createNewSessionService(client), host, project).then(
			(value) => current && setKnown({ place, value }),
			() => current && setKnown({ place, value: null }),
		);
		return () => {
			current = false;
		};
	}, [client, ready, host, project, place, read]);
	return known?.place === place ? known.value : null;
}
