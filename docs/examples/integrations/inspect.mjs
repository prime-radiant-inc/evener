import { createRequire } from 'node:module';
import { AppwireClient } from '../../../appwire-client/typescript/dist/index.js';

// Resolve ws from the SDK checkout installed by npm ci, not a global package.
const require = createRequire(new URL('../../../appwire-client/typescript/package.json', import.meta.url));
const WebSocket = require('ws');
const url = process.env.EVENER_RPC_URL;
const token = process.env.EVENER_HUB_TOKEN;
if (!url || !token) throw new Error('Set EVENER_RPC_URL and EVENER_HUB_TOKEN.');
const client = new AppwireClient({
  url,
  clientInfo: { name: 'documentation-inspector', version: '1' },
  socketFactory: (address) => new WebSocket(address, {
    headers: { Authorization: `Bearer ${token}` },
  }),
});
try {
  const hello = await client.connect();
  const page = await client.request('thread/list', { limit: 20 });
  console.log(JSON.stringify({
    protocolVersion: hello.protocolVersion,
    sessionsOnFirstPage: page.data.length,
    hasMoreSessions: Boolean(page.nextCursor),
  }, null, 2));
} finally {
  client.close();
}
