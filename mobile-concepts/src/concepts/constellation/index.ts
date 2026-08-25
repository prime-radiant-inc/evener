import type { ConceptModule } from "../contract";
import { ConstellationRenderer } from "./ConstellationRenderer";
import "./constellation.css";

export const constellationModule = {
  id: "constellation",
  Renderer: ConstellationRenderer,
} satisfies ConceptModule;
