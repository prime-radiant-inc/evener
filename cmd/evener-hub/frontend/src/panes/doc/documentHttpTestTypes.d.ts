// Test-only builtin surface. The frontend intentionally ships no @types/node.
// Signatures are the fixture-used subsets of @types/node 26.4.1 in the native
// tree, not substitute implementations. Vitest executes Node's real builtins.
declare module "node:http" {
  export interface IncomingMessage {
    url?: string;
    socket: { destroy(error?: Error): unknown };
  }
  export interface ServerResponse {
    statusCode: number;
    readonly destroyed: boolean;
    readonly writableEnded: boolean;
    setHeader(name: string, value: number | string | readonly string[]): this;
    writeHead(statusCode: number, headers?: Record<string, number | string | readonly string[] | undefined>): this;
    end(chunk?: string | Uint8Array): this;
  }
  export interface Server {
    listen(port?: number, hostname?: string, listeningListener?: () => void): this;
    address(): { address: string; family: string; port: number } | string | null;
    closeAllConnections(): void;
    close(callback?: (error?: Error) => void): this;
  }
  export function createServer(listener?: (request: IncomingMessage, response: ServerResponse) => void): Server;
}

declare module "node:events" {
  export function once(emitter: import("node:http").Server, eventName: "listening"): Promise<unknown[]>;
}

declare module "node:fs/promises" {
  export function mkdtemp(prefix: string): Promise<string>;
  export function readFile(path: string, encoding: "utf8"): Promise<string>;
  export function writeFile(path: string, data: string, encoding?: "utf8"): Promise<void>;
  export function rm(path: string, options?: { force?: boolean; recursive?: boolean }): Promise<void>;
}

declare module "node:os" {
  export function tmpdir(): string;
}
