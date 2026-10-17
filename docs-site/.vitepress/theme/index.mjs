import DefaultTheme from "vitepress/theme";
import { h } from "vue";
import "./style.css";
export default {
  extends: DefaultTheme,
  Layout() {
    return h(DefaultTheme.Layout, null, {
      "doc-before": () =>
        h(
          "p",
          { class: "main-notice" },
          "Documentation for main. Released versions may differ.",
        ),
    });
  },
};
