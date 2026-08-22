import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";

const candidates = [
  process.env.CARGO,
  "cargo",
  process.env.HOME ? path.join(process.env.HOME, ".cargo", "bin", "cargo") : undefined,
].filter(Boolean);

let last;
for (const cargo of candidates) {
  if (cargo !== "cargo" && !existsSync(cargo)) continue;
  const result = spawnSync(
    cargo,
    [
      "build",
      "--quiet",
      "--manifest-path",
      "src-tauri/Cargo.toml",
      "--example",
      "appwire_stdio_harness",
    ],
    { cwd: path.resolve(import.meta.dirname, ".."), stdio: "inherit" },
  );
  if (result.error?.code === "ENOENT") {
    last = result.error;
    continue;
  }
  process.exit(result.status ?? 1);
}

throw last ?? new Error("cargo executable not found");
