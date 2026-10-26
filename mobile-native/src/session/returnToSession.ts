// Screens stacked above a session (the Agents or Jobs list, a document) go back to
// it rather than pushing a second copy of it. A subagent's session sits on the
// "Subagent" route, so both session routes count.

type StackState = { index: number; routes: readonly { name: string; params?: object }[] };

export interface SessionNavigation {
	getState(): StackState;
	pop(count: number): void;
	navigate(name: "Conversation", params: { hubId: string; ref: string; title: string }): void;
}

export interface SheetSessionNavigation extends SessionNavigation {
	goBack(): void;
}

const SESSION_ROUTES: ReadonlySet<string> = new Set(["Conversation", "Subagent"]);

/** How many screens to pop to land on this session's own screen, or null
 * when it isn't under the current one. */
export function popsToSession(state: StackState, ref: string): number | null {
	for (let index = state.index - 1; index >= 0; index -= 1) {
		const route = state.routes[index];
		if (route && SESSION_ROUTES.has(route.name) && (route.params as { ref?: unknown } | undefined)?.ref === ref)
			return state.index - index;
	}
	return null;
}

export function returnToSession(
	navigation: SessionNavigation,
	session: { hubId: string; ref: string; title: string },
): void {
	const pops = popsToSession(navigation.getState(), session.ref);
	if (pops !== null) navigation.pop(pops);
	else navigation.navigate("Conversation", session);
}

/** Return from a sheet route. A pop removes the sheet with the screens above
 * the session. When the session isn't under the sheet, the sheet has to go
 * first: a navigate alone would leave it presented over the conversation. */
export function returnToSessionFromSheet(
	sheet: { finish(then?: () => void): void },
	navigation: SheetSessionNavigation,
	session: { hubId: string; ref: string; title: string },
): void {
	const pops = popsToSession(navigation.getState(), session.ref);
	if (pops !== null) {
		sheet.finish(() => navigation.pop(pops));
		return;
	}
	sheet.finish(() => {
		navigation.goBack();
		navigation.navigate("Conversation", session);
	});
}
