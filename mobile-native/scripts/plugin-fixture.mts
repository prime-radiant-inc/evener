import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import WebSocket from "ws";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
import { createHubClient } from "../src/connection";

// The owned hub B proxy is the only target; never use ambient hub credentials.
const client = createHubClient(
  "http://127.0.0.1:9200",
  "",
  (url, options) => new WebSocket(url, options) as unknown as WebSocketLike,
);
const marketplace = "native-mobile-fixture";
const plugin = "native-tools";
try {
  await client.connect();
  if (process.argv[2] === "setup") {
    const existing = await client.request("evener/marketplace/list", {});
    if (existing.marketplaces.some((item) => item.name === marketplace))
      throw Error(
        "Fixture marketplace already exists; inspect before reusing it",
      );
    const root = await mkdtemp(join(tmpdir(), "evener-mobile-plugin-"));
    await mkdir(join(root, ".claude-plugin"));
    await mkdir(join(root, "plugins", plugin, ".claude-plugin"), {
      recursive: true,
    });
    await writeFile(
      join(root, ".claude-plugin", "marketplace.json"),
      JSON.stringify({
        name: marketplace,
        owner: { name: "Native acceptance fixture" },
        plugins: [
          {
            name: plugin,
            source: `./plugins/${plugin}`,
            description:
              "Local native lifecycle fixture without executable content.",
          },
        ],
      }),
    );
    await writeFile(
      join(root, "plugins", plugin, ".claude-plugin", "plugin.json"),
      JSON.stringify({
        name: plugin,
        version: "1.0.0",
        description: "Native lifecycle fixture",
      }),
    );
    await client.request("evener/marketplace/add", {
      name: marketplace,
      source: { kind: "directory", path: root },
    });
    await client.request("evener/plugin/install", { plugin, marketplace });
    console.log({ fixtureSource: root });
  } else if (process.argv[2] !== "status") {
    throw Error("Use setup or status");
  }
  console.log(await client.request("evener/plugin/list", {}));
} finally {
  client.close();
}
