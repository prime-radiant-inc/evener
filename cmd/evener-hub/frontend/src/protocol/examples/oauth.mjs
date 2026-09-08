import { runOAuthCLI } from "./oauth-cli.mjs";

try {
  process.exitCode = await runOAuthCLI();
} catch {
  console.error("OAuth cleanup failed. Inspect the private output file and current credential status.");
  process.exitCode = 1;
}
