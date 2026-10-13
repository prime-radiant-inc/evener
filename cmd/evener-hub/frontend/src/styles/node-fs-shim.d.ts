// This project ships no @types/node (see the note atop
// appwire-client/typescript/reducer.test.ts) and this task may not add it, so
// TypeScript has zero declarations for Node's builtin modules. This file
// hand-declares the exact, minimal surface the test code uses: the files
// token-contract.test.ts and its neighbours read from disk at test time, the
// AsyncLocalStorage testEndedBodyGuard.ts carries the running test in, and
// the git and scratch-repository calls of the copy-only scanner in
// src/dev/copyonly.
// Vitest runs on real Node, so these resolve and execute fine at runtime; this
// file only exists to satisfy `tsc --noEmit`. A real @types/node install, if
// this project ever adds one, supersedes and can delete this file outright.
declare module "node:fs" {
  export interface Dirent {
    name: string;
    isDirectory(): boolean;
    isFile(): boolean;
  }
  export function readFileSync(path: string, encoding: "utf8"): string;
  export function readdirSync(path: string, options: { withFileTypes: true }): Dirent[];
  export function mkdirSync(path: string, options: { recursive: true }): void;
  export function mkdtempSync(prefix: string): string;
  export function rmSync(path: string, options: { recursive: true; force: true }): void;
  export function writeFileSync(path: string, data: string): void;
}

declare module "node:child_process" {
  export function execFileSync(file: string, args: string[], options: { encoding: "utf8"; maxBuffer?: number }): string;
}

declare module "node:process" {
  export const argv: string[];
  export const stdout: { write(chunk: string): boolean };
}

// Node sets import.meta.main on the entry module; src/dev/copyonly/copyonly.ts
// runs its command line only then.
interface ImportMeta {
  readonly main?: boolean;
}

declare module "node:path" {
  export function dirname(path: string): string;
  export function join(...segments: string[]): string;
  export function relative(from: string, to: string): string;
}

declare module "node:url" {
  export function fileURLToPath(url: string): string;
}

declare module "node:async_hooks" {
  export class AsyncLocalStorage<T> {
    getStore(): T | undefined;
    run<R>(store: T, callback: () => R): R;
    disable(): void;
  }
}
