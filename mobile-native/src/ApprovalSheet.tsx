import { useSyncExternalStore } from "react";
import {
  ActivityIndicator,
  Modal,
  Platform,
  ScrollView,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { MobileApproval } from "../../mobile/src/conversation/model";
import type { ApprovalControls } from "./approvalControls";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

export function ApprovalSheet({
  approvals,
  controls,
  hubName,
  close,
  refresh,
}: {
  approvals: MobileApproval[];
  controls: ApprovalControls;
  hubName: string;
  close: () => void;
  refresh: () => Promise<void>;
}) {
  const colors = useColors();
  const state = useSyncExternalStore(controls.subscribe, controls.getSnapshot);
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
              Evener is waiting for your decision about a blocked action.
            </Copy>
          ) : null}
          {state.error ? (
            <View style={{ gap: 8 }}>
              <ErrorMessage message={state.error} />
              <Action
                onPress={() => {
                  void refresh();
                }}
              >
                Refresh session
              </Action>
            </View>
          ) : null}
          {!approvals.length ? <Copy>No approvals pending.</Copy> : null}
          {approvals.map((approval) => (
            <View
              key={approval.id}
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
              <Copy>{approval.path}</Copy>
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
              {approval.output ? (
                <View style={{ gap: 6 }}>
                  <Copy muted>Output so far</Copy>
                  <ScrollView style={{ maxHeight: 180 }} nestedScrollEnabled>
                    <Copy>{approval.output}</Copy>
                  </ScrollView>
                </View>
              ) : null}
              <Copy muted>Allow access for this one action?</Copy>
              <View style={[styles.row, { flexWrap: "wrap" }]}>
                <Action
                  disabled={state.pending !== null}
                  onPress={() => {
                    void controls.resolve(approval, false);
                  }}
                >
                  Deny
                </Action>
                <Action
                  disabled={state.pending !== null}
                  tone="primary"
                  onPress={() => {
                    void controls.resolve(approval, true);
                  }}
                >
                  Allow once
                </Action>
              </View>
              {state.pending === approval.id ? (
                <ActivityIndicator accessibilityLabel="Sending decision" />
              ) : null}
            </View>
          ))}
        </ScrollView>
      </SafeAreaView>
    </Modal>
  );
}
