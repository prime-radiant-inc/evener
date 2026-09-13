import Storage from "expo-sqlite/kv-store";
import { LocationRepository } from "./location";

export const locations = new LocationRepository(Storage);
