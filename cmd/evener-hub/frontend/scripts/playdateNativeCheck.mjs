import { execFileSync } from "node:child_process";
import { accessSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

export function buildNativePairingCheck() {
  const directory = process.env.EVENER_PLAYDATE_CLIENT;
  if (!directory || !path.isAbsolute(directory)) {
    throw new Error("Set EVENER_PLAYDATE_CLIENT to the absolute path of the evener-client-playdate checkout.");
  }
  const client = path.resolve(directory);
  accessSync(path.join(client, "check/pairing.c"));
  const scratch = mkdtempSync(path.join(tmpdir(), "evener-chirp-"));
  const binary = path.join(scratch, "pairing");
  const cleanup = () => rmSync(scratch, { recursive: true, force: true });
  try {
    execFileSync("cc", ["-std=c11", "-O2", "-c", `${client}/vendor/tweetnacl/tweetnacl.c`, "-o", `${scratch}/nacl.o`]);
    execFileSync("cc", [
      "-std=c11",
      "-O2",
      "-Wall",
      "-Wextra",
      "-Werror",
      "-I",
      `${client}/src`,
      "-I",
      `${client}/vendor/tweetnacl`,
      "-I",
      `${client}/vendor/qrcodegen`,
      `${client}/check/pairing.c`,
      `${client}/src/chirp.c`,
      `${client}/src/pairing_protocol.c`,
      `${client}/vendor/qrcodegen/qrcodegen.c`,
      `${scratch}/nacl.o`,
      "-lm",
      "-o",
      binary,
    ]);
    return { client, binary, scratch, cleanup };
  } catch (error) {
    cleanup();
    throw error;
  }
}
