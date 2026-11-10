import { createContext, useContext } from "react";
import type { VoiceClient } from "./voiceClient";
export const VoiceContext = createContext<VoiceClient | null>(null);
export const useVoiceClient = () => useContext(VoiceContext);
