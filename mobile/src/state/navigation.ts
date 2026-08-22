/**
 * Navigation store — three root tabs and a conversation push/pop stack.
 *
 * The root uses a bottom bar with Sessions/New/Settings. A conversation pushes
 * above the tab bar as a focused destination in the history stack with an
 * explicit 44-point back action. V1 does not implement an interactive iOS
 * edge-swipe gesture inside the WebView; `popConversation` is the back action.
 * `clearConversations` is called on profile switch to clear server-scoped
 * placeholder state.
 */
import { create } from "zustand";

export type RootTab = "sessions" | "new" | "settings";

export interface ConversationEntry {
  readonly sessionId: string;
  readonly title: string;
}

export interface NavigationState {
  readonly tab: RootTab;
  readonly conversationStack: readonly ConversationEntry[];
  readonly activeConversation: ConversationEntry | null;
  setTab(tab: RootTab): void;
  pushConversation(entry: ConversationEntry): void;
  popConversation(): void;
  popAllConversations(): void;
  clearConversations(): void;
  canGoBack(): boolean;
}

const VALID_TABS: readonly RootTab[] = ["sessions", "new", "settings"];

export function createNavigationStore() {
  return create<NavigationState>((set, get) => ({
    tab: "sessions",
    conversationStack: [],
    activeConversation: null,
    setTab: (tab) => {
      if (!VALID_TABS.includes(tab)) {
        throw new Error(`unknown tab: ${tab}`);
      }
      set({ tab });
    },
    pushConversation: (entry) =>
      set((state) => ({
        conversationStack: [...state.conversationStack, entry],
        activeConversation: entry,
      })),
    popConversation: () =>
      set((state) => {
        const stack = [...state.conversationStack];
        stack.pop();
        const active = stack.length > 0 ? stack[stack.length - 1] : null;
        if (active === undefined) {
          return { conversationStack: [], activeConversation: null };
        }
        return { conversationStack: stack, activeConversation: active };
      }),
    popAllConversations: () =>
      set({ conversationStack: [], activeConversation: null }),
    clearConversations: () =>
      set({ conversationStack: [], activeConversation: null }),
    canGoBack: () => get().conversationStack.length > 0,
  }));
}
