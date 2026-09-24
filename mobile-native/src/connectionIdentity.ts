/** The connection layer's memory of the hub each client object proved ready
 * under. It lives above every consumer's mount state: a keyed remount resets
 * hook state, so per-instance guards cannot tell that a client still belongs
 * to the hub the connection just re-pointed away from. Written at every
 * birth and adoption, consulted wherever a raw client would otherwise serve
 * a hub. */
const clientReadyHub = new WeakMap<object, string | undefined>();

/** Records the hub a client object proved ready under. */
export function recordClientReadyHub(
	client: object,
	hubId: string | undefined,
): void {
	clientReadyHub.set(client, hubId);
}

/** Whether the client may serve the named hub: a client the record knows
 * proved ready under a different hub is refused until the connection
 * re-points; an unknown client passes exactly as before — a genuinely new
 * client arrives unknown, and only the re-point distinguishes it. */
export function clientServesHub(
	client: object,
	hubId: string | undefined,
): boolean {
	return !clientReadyHub.has(client) || clientReadyHub.get(client) === hubId;
}
