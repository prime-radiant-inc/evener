import type { LiveConceptModule } from "../contract";
import { fieldNotesConversationSkin } from "./ConversationSkin";
import { FieldNotesRenderer } from "./FieldNotesRenderer";
import "./field-notes.css";

export const fieldNotesModule = {
  id: "field-notes",
  label: "Field Notes",
  Renderer: FieldNotesRenderer,
  conversationSkin: fieldNotesConversationSkin,
} satisfies LiveConceptModule;
