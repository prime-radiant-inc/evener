import { fileURLToPath } from "node:url";
export default {
  base: "/evener/",
  outDir: fileURLToPath(
    new URL("../../../.vitepress/mermaid-test-dist", import.meta.url),
  ),
};
