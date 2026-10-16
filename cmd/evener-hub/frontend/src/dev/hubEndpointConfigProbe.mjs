// Node subprocess for hubEndpointConfig.test.ts: this frontend intentionally
// has no @types/node. Observe Vite's actual loader and live proxy in Node,
// return measurements to the TypeScript assertions without a second shim.
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { createServer as createHTTPServer } from "node:http";
import { createInterface } from "node:readline";
import WebSocket, { WebSocketServer } from "ws";

const inspectConfig = `import { loadConfigFromFile } from "vite";
try {
  const loaded = await loadConfigFromFile({command:"serve",mode:"development"}, "vite.config.ts", undefined, "silent", undefined, "runner");
  const proxy = loaded.config.server.proxy;
  console.log(JSON.stringify({api:proxy["/api"],rpc:proxy["/rpc"]}));
} catch (error) {
  console.log(JSON.stringify({code:error.code,field:error.field}));
  process.exitCode = 3;
}`;

function config() {
  const result = spawnSync(process.execPath, ["--input-type=module", "-e", inspectConfig], {
    cwd: process.cwd(),
    env: process.env,
    encoding: "utf8",
  });
  if (result.error) throw result.error;
  return { status: result.status, data: JSON.parse(result.stdout) };
}

async function proxy() {
  const requests = [];
  const backend = createHTTPServer((req, res) => {
    requests.push({ path: req.url, host: req.headers.host, origin: req.headers.origin });
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ path: req.url }));
  });
  const sockets = new WebSocketServer({ server: backend });
  sockets.on("connection", (socket, req) => {
    requests.push({ path: req.url, host: req.headers.host, origin: req.headers.origin });
    socket.send("proxy-ok");
  });
  backend.listen(0, "127.0.0.1");
  await once(backend, "listening");
  const backendPort = backend.address().port;
  const backendOrigin = `http://127.0.0.1:${backendPort}`;
  const serverScript = `import { createServer } from "vite";
const vite = await createServer({ configLoader:"runner", logLevel:"silent", server:{host:"127.0.0.1",port:0} });
await vite.listen();
console.log(JSON.stringify({port:vite.httpServer.address().port,api:vite.config.server.proxy["/api"],rpc:vite.config.server.proxy["/rpc"]}));
process.stdin.resume();
process.stdin.on("end", () => vite.close());`;
  const child = spawn(process.execPath, ["--input-type=module", "-e", serverScript], {
    cwd: process.cwd(),
    env: { ...process.env, EVENER_HUB_URL: `${backendOrigin}/`, EVENER_HUB_ADDR: undefined },
    stdio: ["pipe", "pipe", "pipe"],
  });
  const lines = createInterface({ input: child.stdout });
  const childExit = once(child, "exit");
  let ws;
  try {
    const [line] = await Promise.race([
      once(lines, "line"),
      childExit.then(() => {
        throw new Error("Vite exited before listening");
      }),
    ]);
    const loaded = JSON.parse(line);
    const viteOrigin = `http://127.0.0.1:${loaded.port}`;
    const response = await fetch(`${viteOrigin}/api/proxy-check`, { headers: { Origin: viteOrigin } });
    const http = { status: response.status, body: await response.json() };
    ws = new WebSocket(`${viteOrigin.replace("http:", "ws:")}/rpc`, { headers: { Origin: viteOrigin } });
    const [message] = await once(ws, "message");
    return { loaded, backendOrigin, backendPort, viteOrigin, http, wsMessage: String(message), requests };
  } finally {
    ws?.terminate();
    child.stdin.end();
    await childExit;
    lines.close();
    await new Promise((resolve, reject) => sockets.close((error) => (error ? reject(error) : resolve())));
    await new Promise((resolve, reject) => backend.close((error) => (error ? reject(error) : resolve())));
  }
}

console.log(JSON.stringify(process.argv[2] === "proxy" ? await proxy() : config()));
