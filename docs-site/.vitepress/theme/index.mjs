import DefaultTheme from "vitepress/theme";
import { useRoute } from "vitepress";
import { defineComponent, h, nextTick, onMounted, watch } from "vue";
import "./style.css";

let renderer;
let diagramID = 0;
async function renderDiagrams() {
  await nextTick();
  const blocks = [...document.querySelectorAll(".vp-doc .language-mermaid")];
  if (!blocks.length) return;
  renderer ||= import("mermaid").then(({ default: mermaid }) => {
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict" });
    return mermaid;
  });
  const mermaid = await renderer;
  for (const block of blocks) {
    if (!block.isConnected) continue;
    const source = block.querySelector("code").textContent;
    const { svg } = await mermaid.render(
      `evener-diagram-${diagramID++}`,
      source,
    );
    if (!block.isConnected) continue;
    const diagram = document.createElement("div");
    diagram.className = "mermaid-diagram";
    diagram.innerHTML = svg;
    block.replaceWith(diagram);
  }
}

export default {
  extends: DefaultTheme,
  Layout: defineComponent({
    setup() {
      const route = useRoute();
      onMounted(renderDiagrams);
      watch(() => route.path, renderDiagrams, { flush: "post" });
      return () =>
        h(DefaultTheme.Layout, null, {
          "doc-before": () =>
            h(
              "p",
              { class: "main-notice" },
              "Documentation for main. Released versions may differ.",
            ),
        });
    },
  }),
};
