import { Storage } from "expo-sqlite/kv-store";
import { HubUpgradeRepository } from "./hubUpgradeRepository";
export const nativeHubUpgradeStorage = new HubUpgradeRepository(Storage);
