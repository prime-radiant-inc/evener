import type { ConceptModule } from "../contract";
import { StillwaterRenderer } from "./StillwaterRenderer";
import "./stillwater.css";

export const stillwaterModule = {
  id: "stillwater",
  Renderer: StillwaterRenderer,
} satisfies ConceptModule;
