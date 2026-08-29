import type { LiveConceptModule } from "../contract";
import { stillwaterConversationSkin } from "./ConversationSkin";
import { StillwaterRenderer } from "./StillwaterRenderer";
import "./stillwater.css";

export const stillwaterModule = {
  id: "stillwater",
  label: "Stillwater",
  Renderer: StillwaterRenderer,
  conversationSkin: stillwaterConversationSkin,
} satisfies LiveConceptModule;
