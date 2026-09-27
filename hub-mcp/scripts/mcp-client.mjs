#!/usr/bin/env node
// mcp-client is a minimal, dependency-free MCP client speaking the raw
// JSON-RPC-over-stdio protocol. The e2e driver (and any shell) uses it to
// exercise the hub MCP server exactly the way a session's daemon does —
// initialize, tools/list, tools/call — without an SDK on the client side,
// which keeps the interop check honest: if this hand-rolled client and the
// Go SDK in the daemon both work, the server's protocol is right.
//
// Usage:
//   import { McpClient } from "./mcp-client.mjs";
//   const client = await McpClient.spawn(["node", "dist/src/index.js"], { env });
//   const tools = await client.listTools();
//   const result = await client.callTool("hub_overview", {});
//   await client.close();

import { spawn } from "node:child_process";

export class McpClient {
  constructor(process, nextId) {
    this.proc = process;
    this.nextId = nextId;
    this.pending = new Map();
    this.buffer = "";
  }

  static spawn(command, { env = process.env } = {}) {
    return new Promise((resolve, reject) => {
      const proc = spawn(command[0], command.slice(1), {
        env: { ...env },
        stdio: ["pipe", "pipe", "inherit"],
      });
      proc.on("error", reject);
      // Never leave the spawned server holding this process's pipes open: if
      // the driver dies mid-workflow (an assertion, an unhandled rejection),
      // the child must die with it rather than linger as an orphan.
      process.once("exit", () => {
        proc.kill("SIGKILL");
      });
      const client = new McpClient(proc, 1);
      proc.stdout.on("data", (chunk) => client.feed(chunk));
      proc.stdout.once("readable", () => resolve(client));
      const failTimer = setTimeout(() => reject(new Error("server produced no output in 10s")), 10_000);
      client
        .request("initialize", {
          protocolVersion: "2025-06-18",
          capabilities: {},
          clientInfo: { name: "hub-mcp-e2e", version: "0.0.0" },
        })
        .then(() => {
          clearTimeout(failTimer);
          client.notify("notifications/initialized", {});
          resolve(client);
        }, reject);
    });
  }

  feed(chunk) {
    this.buffer += chunk.toString("utf8");
    let index = this.buffer.indexOf("\n");
    while (index !== -1) {
      const line = this.buffer.slice(0, index).trim();
      this.buffer = this.buffer.slice(index + 1);
      if (line) {
        try {
          this.dispatch(JSON.parse(line));
        } catch {
          /* a partial line will complete on the next chunk */
        }
      }
      index = this.buffer.indexOf("\n");
    }
  }

  dispatch(message) {
    if (typeof message.id === "number" && this.pending.has(message.id)) {
      const { resolve, reject } = this.pending.get(message.id);
      this.pending.delete(message.id);
      if (message.error) {
        reject(new Error(`MCP ${message.error.code}: ${message.error.message}`));
      } else {
        resolve(message.result);
      }
    }
  }

  request(method, params) {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.proc.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id, method, params })}\n`);
    });
  }

  notify(method, params) {
    this.proc.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", method, params })}\n`);
  }

  async listTools() {
    const result = await this.request("tools/list", {});
    return result.tools;
  }

  async callTool(name, args) {
    const result = await this.request("tools/call", { name, arguments: args });
    const text = (result.content ?? [])
      .filter((block) => block.type === "text")
      .map((block) => block.text)
      .join("\n");
    return { text, isError: result.isError === true };
  }

  async close() {
    this.proc.stdin.end();
    await new Promise((resolve) => {
      this.proc.once("exit", resolve);
      this.proc.kill("SIGTERM");
    });
  }
}
