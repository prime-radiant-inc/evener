import type { LiveConceptModule } from "../contract";
import { StillwaterRenderer } from "./StillwaterRenderer";
import "./stillwater.css";

export const stillwaterModule = {
  id: "stillwater",
  label: "Stillwater",
  Renderer: StillwaterRenderer,
} satisfies LiveConceptModule;
