import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { once } from 'node:events';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const require = createRequire(new URL('../../../appwire-client/typescript/package.json', import.meta.url));
const { WebSocketServer } = require('ws');
const hello = JSON.parse(await readFile(new URL('./initialize-result.json', import.meta.url), 'utf8'));

test('inspection authenticates, initializes and reads a bounded page', async () => {
  const server = new WebSocketServer({ host: '127.0.0.1', port: 0 });
  const methods = [];
  let peerError;
  server.on('connection', (socket, request) => {
    try { assert.equal(request.headers.authorization, 'Bearer fixture-token'); }
    catch (error) { peerError = error; socket.close(); return; }
    socket.on('message', (bytes) => {
      try {
        const frame = JSON.parse(bytes.toString());
        methods.push(frame.method);
        if (frame.method === 'initialized') {
          assert.equal(frame.id, undefined);
          return;
        }
        let result;
        if (frame.method === 'initialize') {
          assert.equal(frame.params.protocolVersion, hello.protocolVersion);
          result = hello;
        } else {
          assert.equal(frame.method, 'thread/list');
          assert.deepEqual(frame.params, { limit: 20 });
          result = { data: [], nextCursor: 'opaque-next' };
        }
        socket.send(JSON.stringify({ id: frame.id, result }));
      } catch (error) { peerError = error; socket.close(); }
    });
  });
  try {
    await once(server, 'listening');
    const { stdout } = await promisify(execFile)(process.execPath, [fileURLToPath(new URL('./inspect.mjs', import.meta.url))], {
      env: { ...process.env, EVENER_RPC_URL: `ws://127.0.0.1:${server.address().port}/rpc`, EVENER_HUB_TOKEN: 'fixture-token' },
      timeout: 15000,
    });
    if (peerError) throw peerError;
    assert.deepEqual(methods, ['initialize', 'initialized', 'thread/list']);
    assert.deepEqual(JSON.parse(stdout), { protocolVersion: hello.protocolVersion, sessionsOnFirstPage: 0, hasMoreSessions: true });
  } finally {
    for (const socket of server.clients) socket.terminate();
    await new Promise((resolve) => server.close(resolve));
  }
});
