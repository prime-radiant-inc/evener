import { Storage } from "expo-sqlite/kv-store";
import { ReaderPositionRepository } from "./readerPosition";

export const readerPositions = new ReaderPositionRepository(Storage);
