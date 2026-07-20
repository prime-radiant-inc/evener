import { defineConfig } from "eslint/config";
import tseslint from "typescript-eslint";

export default defineConfig({
  files: ["src/**/*.ts", "src/**/*.tsx"],
  extends: [tseslint.configs.recommended],
});
