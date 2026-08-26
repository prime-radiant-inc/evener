import type { LiveConceptModule } from "../contract";
import { FieldNotesRenderer } from "./FieldNotesRenderer";
import "./field-notes.css";

export const fieldNotesModule = {
  id: "field-notes",
  label: "Field Notes",
  Renderer: FieldNotesRenderer,
} satisfies LiveConceptModule;
