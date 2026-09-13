// Explicit read-only smoke check. Credentials are loaded from a file, never logged.
import { readFileSync } from "node:fs";
import WebSocket from "ws";
import { createHubClient } from "../src/connection";
import { createConversationService } from "../../mobile/src/services/conversation";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
const [origin, tokenFile] = process.argv.slice(2);
if (!origin || !tokenFile)
	throw new Error("Usage: tsx scripts/check-hub.ts ORIGIN TOKEN_FILE");
const client = createHubClient(
	origin,
	readFileSync(tokenFile, "utf8").trim(),
	(url, options) => new WebSocket(url, options) as unknown as WebSocketLike,
);
try {
	const result = await client.connect();
	console.log("Handshake:", result.protocolVersion);
	const roster = await client.request("thread/list", { limit: 5 });
	console.log("Sessions returned:", roster.data.length);
	const first = roster.data[0];
	if (first) {
  const service=createConversationService(client);
  const conversation=await service.open(first.evener.ref);
  console.log('Conversation projected:',conversation.items.length,'items');
  service.close();
	}
} finally {
	client.close();
}
