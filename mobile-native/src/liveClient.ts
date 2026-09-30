/** The connection's client while it's live on `hubId`, else null: what a
 * reader of that hub's data binds to. Apart from ConnectionProvider, so the
 * screens' tests that mock the provider keep the real rule. */
export function liveClientFor<Client>(
	connection: { client: Client | null; state: string; activeProfile: { id: string } | null },
	hubId: string,
): Client | null {
	return connection.state === "ready" && connection.activeProfile?.id === hubId ? connection.client : null;
}
