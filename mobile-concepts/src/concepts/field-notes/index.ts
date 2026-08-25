import type { ConceptModule } from "../contract";
import { FieldNotesRenderer } from "./FieldNotesRenderer";
import "./field-notes.css";

export const fieldNotesModule = {
  id: "field-notes",
  Renderer: FieldNotesRenderer,
} satisfies ConceptModule;
