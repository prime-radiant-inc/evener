import { lstat, open, readFile, unlink } from "node:fs/promises";
import { managementErrorMessage } from "./management-recovery.mjs";
import { captureOAuthInput, runOAuth, safeOAuthSummary } from "./oauth-logic.mjs";

export async function runOAuthCLI({ env = process.env, connect, stdout = console.log, stderr = console.error } = {}) {
  const environment = { ...env };
  let hub;
  let output;
  let reserved;
  let keepOutput = false;
  let flowStarted = false;
  const outputPath = environment.EVENER_OAUTH_OUTPUT_FILE;
  let exitCode = 1;
  try {
    const action = environment.EVENER_OAUTH_ACTION;
    const params = JSON.parse(await readFile(environment.EVENER_OAUTH_PARAMS_FILE, "utf8"));
    const options = { action, params, ownedHub: environment.EVENER_OAUTH_OWNED_HUB, environment };
    captureOAuthInput(options);
    if (action.endsWith("/start")) {
      if (!outputPath) throw new Error("Provide a private OAuth output file.");
      // Reserve the destination before creating a flow; an existing file is never overwritten.
      output = await open(outputPath, "wx", 0o600);
      reserved = await output.stat();
    }
    const factory = connect ?? (await import("./connection.mjs")).clientFromEnvironment;
    ({ hub } = factory(environment));
    const result = await runOAuth(hub, options);
    if (result.outcome === "started") {
      flowStarted = true;
      await output.writeFile(`${JSON.stringify(result, null, 2)}\n`, "utf8");
      await output.sync();
      keepOutput = true;
    }
    stdout(JSON.stringify(safeOAuthSummary(result)));
    exitCode = result.outcome === "uncertain" ? 2 : 0;
  } catch (error) {
    stderr(
      flowStarted
        ? JSON.stringify({ outcome: "started", challenge: "write_unconfirmed" })
        : managementErrorMessage(
            error,
            "OAuth operation could not be confirmed. Inspect current credential status before deliberately trying again.",
          ),
    );
  } finally {
    hub?.close();
    if (output) {
      await output.close();
      if (!keepOutput) {
        const current = await lstat(outputPath).catch((error) => {
          if (error.code === "ENOENT") return null;
          throw error;
        });
        if (current && current.dev === reserved.dev && current.ino === reserved.ino && current.size === 0)
          await unlink(outputPath);
      }
    }
  }
  return exitCode;
}
