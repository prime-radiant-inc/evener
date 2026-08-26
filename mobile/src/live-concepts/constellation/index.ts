import type { LiveConceptModule } from "../contract";
import { ConstellationRenderer } from "./ConstellationRenderer";
import "./constellation.css";

export const constellationModule = {
  id: "constellation",
  label: "Constellation",
  Renderer: ConstellationRenderer,
} satisfies LiveConceptModule;
