import { describe, expect, it } from "vitest";
import { createNavigationStore } from "./navigation";

describe("navigation store — conversation semantics", () => {
  it("pushConversation sets activeConversation and canGoBack", () => {
    const store = createNavigationStore();
    store.getState().pushConversation({ sessionId: "s1", title: "T1" });
    expect(store.getState().activeConversation?.sessionId).toBe("s1");
    expect(store.getState().canGoBack()).toBe(true);
  });

  it("popConversation returns to tab when stack empties", () => {
    const store = createNavigationStore();
    store.getState().pushConversation({ sessionId: "s1", title: "T1" });
    store.getState().popConversation();
    expect(store.getState().activeConversation).toBeNull();
    expect(store.getState().canGoBack()).toBe(false);
  });

  it("popAllConversations clears the stack", () => {
    const store = createNavigationStore();
    store.getState().pushConversation({ sessionId: "s1", title: "A" });
    store.getState().pushConversation({ sessionId: "s2", title: "B" });
    store.getState().popAllConversations();
    expect(store.getState().conversationStack).toEqual([]);
    expect(store.getState().activeConversation).toBeNull();
  });

  it("tab is preserved across conversation push/pop", () => {
    const store = createNavigationStore();
    store.getState().setTab("new");
    store.getState().pushConversation({ sessionId: "s1", title: "A" });
    expect(store.getState().tab).toBe("new");
    store.getState().popConversation();
    expect(store.getState().tab).toBe("new");
  });

  it("switching profile via onboarding clears conversation stack", () => {
    const store = createNavigationStore();
    store.getState().pushConversation({ sessionId: "s1", title: "A" });
    store.getState().clearConversations();
    expect(store.getState().conversationStack).toEqual([]);
  });
});
