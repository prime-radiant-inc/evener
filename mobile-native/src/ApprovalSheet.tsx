import { useSyncExternalStore } from "react";
import {
  ActivityIndicator,
  Modal,
  Platform,
  ScrollView,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { SandboxEscalationRequested } from "@evener/appwire-client";
import type { ApprovalControls } from "./approvalControls";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ApprovalSheet({
  approvals,
  controls,
  hubName,
  close,
}: {
  approvals: SandboxEscalationRequested[];
  controls: ApprovalControls;
  hubName: string;
  close: () => void;
}) {
  const colors = useColors();
  const state = useSyncExternalStore(controls.subscribe, controls.getSnapshot);
  const decisionDisabled =
    state.pending !== null || state.refreshing || state.error !== null;
  return (
    <Modal
      animationType="slide"
      presentationStyle={Platform.OS === "ios" ? "pageSheet" : "fullScreen"}
      onRequestClose={close}
    >
      <SafeAreaView
        style={[styles.fill, { backgroundColor: colors.background }]}
      >
        <View
          style={[
            styles.row,
            {
              paddingHorizontal: 20,
              paddingVertical: 8,
              borderBottomWidth: 1,
              borderColor: colors.border,
            },
          ]}
        >
          <View style={styles.fill}>
            <Copy>Sandbox approvals</Copy>
            <Copy muted>{hubName}</Copy>
          </View>
          <Action onPress={close}>Done</Action>
        </View>
        <ScrollView contentContainerStyle={{ padding: 20, gap: 24 }}>
          {approvals.length ? (
            <Copy muted>
              Requested by Evener, not the agent. The sandbox blocked this
              action and needs your decision.
            </Copy>
          ) : null}
          {state.error ? (
            <View style={{ gap: 8 }}>
              <ErrorMessage message={state.error} />
              <Action
                disabled={state.refreshing || state.pending !== null}
                onPress={() => {
                  void controls.refresh();
                }}
              >
                {state.refreshing ? "Refreshing session…" : "Refresh session"}
              </Action>
            </View>
          ) : null}
          {!approvals.length ? <Copy>No approvals pending.</Copy> : null}
          {approvals.map((approval) => (
            <View
              key={approval.escalationId}
              style={{
                gap: 12,
                borderBottomWidth: 1,
                borderColor: colors.border,
                paddingBottom: 20,
              }}
            >
              <Copy>
                {approval.tool} · {approval.kind}
              </Copy>
              <Copy muted>Blocked path</Copy>
              <Copy>{approval.deniedPath}</Copy>
              <Copy muted>Sandbox · {approval.mode}</Copy>
              {approval.command ? (
                <View style={{ gap: 6 }}>
                  <Copy muted>Command</Copy>
                  <Copy>{approval.command}</Copy>
                </View>
              ) : null}
              {approval.partiallyRan ? (
                <Copy>
                  Part of this command may already have run before the sandbox
                  blocked it.
                </Copy>
              ) : null}
              {approval.outputSoFar ? (
                <View style={{ gap: 6 }}>
                  <Copy muted>Output so far</Copy>
                  <ScrollView style={{ maxHeight: 180 }} nestedScrollEnabled>
                    <Copy>{approval.outputSoFar}</Copy>
                  </ScrollView>
                </View>
              ) : null}
              <Copy muted>Allow access for this one action?</Copy>
              <View style={[styles.row, { flexWrap: "wrap" }]}>
                <Action
                  disabled={decisionDisabled}
                  onPress={() => {
                    void controls.resolve(approval, false);
                  }}
                >
                  Deny
                </Action>
                <Action
                  disabled={decisionDisabled}
                  tone="primary"
                  onPress={() => {
                    void controls.resolve(approval, true);
                  }}
                >
                  Allow once
                </Action>
              </View>
              {state.pending === approval.escalationId ? (
                <ActivityIndicator accessibilityLabel="Sending decision" />
              ) : null}
            </View>
          ))}
        </ScrollView>
      </SafeAreaView>
    </Modal>
  );
}
