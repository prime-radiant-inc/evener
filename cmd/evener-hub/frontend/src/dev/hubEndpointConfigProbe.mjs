// Node subprocess for hubEndpointConfig.test.ts: this frontend intentionally
// has no @types/node. Observe Vite's actual loader and live proxy in Node,
// return measurements to the TypeScript assertions without a second shim.
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { createServer as createHTTPServer } from "node:http";
import { createInterface } from "node:readline";
import WebSocket, { WebSocketServer } from "ws";

const inspectConfig = `import { loadConfigFromFile } from "vite";
if (process.env.EVENER_PROBE_STALL_CONFIG === "1") await new Promise(() => setInterval(() => {}, 1000));
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
    timeout: 6_000,
  });
  if (result.error?.code === "ETIMEDOUT") return { timeout: "configChild" };
  if (result.error) throw result.error;
  return { status: result.status, data: JSON.parse(result.stdout) };
}

async function within(promise, phase, milliseconds) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(
          () => reject(Object.assign(new Error(`probe timed out: ${phase}`), { phase })),
          milliseconds,
        );
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
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
    if (process.argv[2] !== "proxy-stall") socket.send("proxy-ok");
  });
  const serverScript = `import { createServer } from "vite";
const vite = await createServer({ configLoader:"runner", logLevel:"silent", server:{host:"127.0.0.1",port:0} });
await vite.listen();
console.log(JSON.stringify({port:vite.httpServer.address().port,api:vite.config.server.proxy["/api"],rpc:vite.config.server.proxy["/rpc"]}));
process.stdin.resume();
process.stdin.on("end", () => vite.close());`;
  let child;
  let childExit;
  let lines;
  let ws;
  let backendPort;
  let vitePort;
  let failure;
  let measured;
  const onTerminate = () => {
    ws?.terminate();
    child?.kill("SIGKILL");
    for (const client of sockets.clients) client.terminate();
    backend.close();
    if (childExit)
      void within(childExit, "viteKill", 2_000).then(
        () => process.exit(1),
        () => process.exit(1),
      );
    else process.exit(1);
  };
  process.once("SIGTERM", onTerminate);
  try {
    const listening = once(backend, "listening");
    backend.listen(0, "127.0.0.1");
    await within(listening, "backendReady", 3_000);
    backendPort = backend.address().port;
    const backendOrigin = `http://127.0.0.1:${backendPort}`;
    child = spawn(process.execPath, ["--input-type=module", "-e", serverScript], {
      cwd: process.cwd(),
      env: { ...process.env, EVENER_HUB_URL: `${backendOrigin}/`, EVENER_HUB_ADDR: undefined },
      stdio: ["pipe", "pipe", "pipe"],
    });
    child.stdin.on("error", () => {}); // Child may exit before teardown closes stdin.
    lines = createInterface({ input: child.stdout });
    childExit = once(child, "exit");
    const [line] = await within(
      Promise.race([
        once(lines, "line"),
        childExit.then(() => {
          throw new Error("Vite exited before listening");
        }),
      ]),
      "viteReady",
      8_000,
    );
    const loaded = JSON.parse(line);
    vitePort = loaded.port;
    const viteOrigin = `http://127.0.0.1:${loaded.port}`;
    const signal = AbortSignal.timeout(4_000);
    const response = await within(
      fetch(`${viteOrigin}/api/proxy-check`, { headers: { Origin: viteOrigin }, signal }),
      "httpResponse",
      4_000,
    );
    const http = { status: response.status, body: await within(response.json(), "httpBody", 4_000) };
    ws = new WebSocket(`${viteOrigin.replace("http:", "ws:")}/rpc`, { headers: { Origin: viteOrigin } });
    const [message] = await within(once(ws, "message"), "wsMessage", 2_000);
    if (process.argv[2] === "proxy-outer-stall") {
      console.log(JSON.stringify({ backendPort, vitePort, vitePid: child.pid }));
      await new Promise(() => setInterval(() => {}, 1_000));
    }
    measured = {
      loaded,
      backendOrigin,
      backendPort,
      vitePort,
      vitePid: child.pid,
      viteOrigin,
      http,
      wsMessage: String(message),
      requests,
      shutdownForced: false,
    };
    return measured;
  } catch (error) {
    if (process.argv[2] !== "proxy-stall" || error.phase !== "wsMessage") throw error;
    failure = error.phase;
  } finally {
    ws?.terminate();
    try {
      if (child) {
        // Withhold the close signal in this test mode to exercise forced shutdown.
        if (process.argv[2] !== "proxy-shutdown-stall") child.stdin.end();
        try {
          await within(childExit, "viteClose", 2_000);
        } catch {
          if (measured) measured.shutdownForced = true;
          child.kill("SIGTERM");
          try {
            await within(childExit, "viteTerminate", 2_000);
          } catch {
            child.kill("SIGKILL");
            await within(childExit, "viteKill", 2_000);
          }
        }
      }
    } finally {
      lines?.close();
      for (const client of sockets.clients) client.terminate();
      try {
        await within(
          new Promise((resolve, reject) => sockets.close((error) => (error ? reject(error) : resolve()))),
          "wsClose",
          2_000,
        );
      } finally {
        backend.closeAllConnections();
        if (backend.listening)
          await within(
            new Promise((resolve, reject) => backend.close((error) => (error ? reject(error) : resolve()))),
            "backendClose",
            2_000,
          );
        process.removeListener("SIGTERM", onTerminate);
      }
    }
  }
  return { timeout: failure, backendPort, vitePort, vitePid: child.pid };
}

async function outerStall() {
  const script = `await new Promise(resolve => setTimeout(resolve, ${Number(process.env.EVENER_PROBE_STARTUP_DELAY)}));
process.argv[2] = "proxy-outer-stall";
await import(${JSON.stringify(import.meta.url)});`;
  const child = spawn(process.execPath, ["--input-type=module", "-e", script], { env: process.env });
  const closed = once(child, "close");
  const lines = createInterface({ input: child.stdout });
  let stderr = "";
  child.stderr.setEncoding("utf8").on("data", (chunk) => {
    stderr += chunk;
  });
  let timer;
  let timedOut = false;
  try {
    const [line] = await within(
      Promise.race([
        once(lines, "line"),
        closed.then(() => {
          throw new Error(`probe exited before readiness: ${stderr}`);
        }),
      ]),
      "outerReady",
      25_000,
    );
    // The probe emits this JSON line only after Vite, HTTP and WS are ready.
    // Startup has its own bound, the stall timeout must not include it.
    timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
    }, 3_000);
    const [code, signal] = await within(closed, "outerTerminate", 6_000);
    return { timedOut, code, signal, ...JSON.parse(line) };
  } finally {
    clearTimeout(timer);
    lines.close();
    child.kill("SIGTERM");
    try {
      await within(closed, "outerClose", 3_000);
    } catch {
      child.kill("SIGKILL");
      await within(closed, "outerKill", 3_000);
    }
  }
}

async function cleanup() {
  async function available(port) {
    const server = createHTTPServer();
    const listening = once(server, "listening");
    server.listen(Number(port), "127.0.0.1");
    await within(listening, "checkPort", 2_000);
    await within(new Promise((resolve) => server.close(resolve)), "checkClose", 2_000);
    return true;
  }
  let viteAlive = true;
  try {
    process.kill(Number(process.env.PROBE_VITE_PID), 0);
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
    viteAlive = false;
  }
  return {
    viteAlive,
    backendAvailable: await available(process.env.PROBE_BACKEND_PORT),
    viteAvailable: await available(process.env.PROBE_VITE_PORT),
  };
}

let result;
if (process.argv[2] === "cleanup") {
  result = await cleanup();
} else if (process.argv[2] === "outer-stall") {
  result = await outerStall();
} else if (process.argv[2].startsWith("proxy")) {
  result = await proxy();
} else {
  result = config();
}
console.log(JSON.stringify(result));
