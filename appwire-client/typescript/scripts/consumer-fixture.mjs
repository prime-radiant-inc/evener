import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

// createConsumerDir makes the throwaway consumer the package qualification
// installs into. Without a local package.json, npm adopts the nearest ancestor
// holding package.json or node_modules as its prefix and installs outside the
// fixture; a private manifest keeps the install in the directory this run owns.
export function createConsumerDir(baseDir = tmpdir()) {
  const dir = mkdtempSync(join(baseDir, "evener-appwire-package-"));
  writeFileSync(
    join(dir, "package.json"),
    `${JSON.stringify({ name: "evener-appwire-package-consumer", private: true }, null, 2)}\n`,
  );
  return dir;
}
