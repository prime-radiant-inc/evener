import type { LiveConceptModule } from "../contract";
import { ConstellationRenderer } from "./ConstellationRenderer";
import { constellationConversationSkin } from "./ConversationSkin";
import "./constellation.css";

export const constellationModule = {
  id: "constellation",
  label: "Constellation",
  Renderer: ConstellationRenderer,
  conversationSkin: constellationConversationSkin,
} satisfies LiveConceptModule;
