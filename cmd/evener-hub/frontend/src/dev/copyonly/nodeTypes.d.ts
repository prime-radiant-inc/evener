// The project ships no @types/node (see src/styles/node-fs-shim.d.ts), so this
// declares the minimal surface of Node's builtins the copy-only scanner and
// its tests use, beside the shared shims' readFileSync, path helpers and
// tmpdir. Vitest runs on real Node; this file only satisfies `tsc --noEmit`.
declare module "node:child_process" {
  export function execFileSync(file: string, args: string[], options: { encoding: "utf8"; maxBuffer?: number }): string;
}

declare module "node:fs" {
  export function mkdirSync(path: string, options: { recursive: true }): void;
  export function mkdtempSync(prefix: string): string;
  export function rmSync(path: string, options: { recursive: true; force: true }): void;
  export function writeFileSync(path: string, data: string): void;
}
