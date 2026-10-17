import { prepare } from "./prepare.mjs";
const result = await prepare();
console.log(
  `Validated and staged ${result.pages} pages and ${result.assets} assets, including Markdown exports and llms.txt.`,
);
