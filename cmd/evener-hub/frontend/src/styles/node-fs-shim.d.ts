// This project ships no @types/node (see the note atop
// src/protocol/reducer.test.ts) and this task may not add it, so
// TypeScript has zero declarations for Node's builtin modules. This file
// hand-declares the exact, minimal surface token-contract.test.ts uses to
// walk and read the src/ tree from disk at test time (vitest runs on real
// Node, so these resolve and execute fine at runtime — this file only
// exists to satisfy `tsc --noEmit`). A real @types/node install, if this
// project ever adds one, supersedes and can delete this file outright.
declare module "node:fs" {
  export interface Dirent {
    name: string;
    isDirectory(): boolean;
    isFile(): boolean;
  }
  export function readFileSync(path: string, encoding: "utf8"): string;
  export function readdirSync(path: string, options: { withFileTypes: true }): Dirent[];
}

declare module "node:path" {
  export function dirname(path: string): string;
  export function join(...segments: string[]): string;
  export function relative(from: string, to: string): string;
}

declare module "node:url" {
  export function fileURLToPath(url: string): string;
}
