import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitepress";
import { slug } from "github-slugger";
const manifest = JSON.parse(
  readFileSync(new URL("../publication.json", import.meta.url), "utf8"),
);
const groups = [...new Set(manifest.pages.map((p) => p.group))];
export default defineConfig({
  title: "Evener",
  description: "Evener documentation for main",
  base: "/evener/",
  srcDir: ".generated",
  outDir: ".vitepress/dist",
  srcExclude: ["public/**"],
  vite: {
    publicDir: fileURLToPath(new URL("../.generated/public", import.meta.url)),
  },
  transformPageData(page) {
    page.frontmatter.source = manifest.pages.find(
      (p) => p.path === page.relativePath,
    )?.source;
  },
  markdown: { anchor: { slugify: slug } },
  themeConfig: {
    search: { provider: "local" },
    nav: [
      { text: "Documentation", link: "/" },
      { text: "Markdown", link: "/evener/llms.txt" },
    ],
    sidebar: groups.map((group) => ({
      text: group,
      items: manifest.pages
        .filter((p) => p.group === group)
        .map((p) => ({
          text: p.title,
          link: "/" + p.path.replace(/\.md$/, ".html"),
        })),
    })),
    socialLinks: [
      { icon: "github", link: "https://github.com/prime-radiant-inc/evener" },
    ],
    editLink: {
      pattern: ({ frontmatter }) =>
        frontmatter.source
          ? `https://github.com/prime-radiant-inc/evener/edit/main/${frontmatter.source}`
          : "",
    },
    footer: { message: "Documentation tracks main. Releases may differ." },
  },
});
