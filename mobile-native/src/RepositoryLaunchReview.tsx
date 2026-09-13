import { useState } from "react";
import { Modal, ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { RepoLaunchConfigStatus } from "../../appwire-client/typescript/types.gen";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";

const descriptions: Record<string, string> = {
  trusted: "Trusted repository configuration",
  untrusted: "Repository configuration needs review",
  changed: "Repository configuration has changed",
  rejected: "Repository configuration was rejected",
};

export function RepositoryLaunchReview({
  repo,
  hubName,
  disabled,
  error,
  trust,
}: {
  repo: RepoLaunchConfigStatus | undefined;
  hubName: string;
  disabled: boolean;
  error: string | null;
  trust(hash: string): Promise<boolean>;
}) {
  const colors = useColors();
  const [review, setReview] = useState<RepoLaunchConfigStatus | null>(null);
  const changed = !!review && review.hash !== repo?.hash;
  return (
    <>
      {repo && repo.trust !== "absent" && (
        <Action disabled={disabled} onPress={() => setReview({ ...repo })}>
          {descriptions[repo.trust] ?? "Review repository configuration"}
        </Action>
      )}
      <Modal
        visible={!!review}
        animationType="slide"
        onRequestClose={() => {
          setReview(null);
        }}
      >
        <SafeAreaView
          style={[styles.fill, { backgroundColor: colors.background }]}
        >
          <View style={[styles.row, { paddingHorizontal: 16 }]}>
            <Action onPress={() => setReview(null)}>Close</Action>
          </View>
          <ScrollView contentContainerStyle={{ padding: 20, gap: 16 }}>
            <Copy>Repository configuration</Copy>
            <Copy muted>{hubName}</Copy>
            <Copy muted>{review?.path}</Copy>
            <Copy>{descriptions[repo?.trust ?? ""] ?? "Review required"}</Copy>
            <Copy muted>
              The hub applies this file to new sessions only after you trust it.
              Review its settings before continuing.
            </Copy>
            <Copy>
              {review?.preview ?? "The hub did not provide a preview."}
            </Copy>
            {review?.hash && <Copy muted>Reviewed hash: {review.hash}</Copy>}
            {changed && (
              <Copy>
                The file changed. Close this review and open the current
                version.
              </Copy>
            )}
            <ErrorMessage message={error} />
            {repo?.trust !== "trusted" && (
              <Action
                disabled={
                  disabled ||
                  changed ||
                  !review?.hash ||
                  !review.preview ||
                  !["untrusted", "changed", "rejected"].includes(
                    repo?.trust ?? "",
                  )
                }
                onPress={() => {
                  if (review?.hash) void trust(review.hash);
                }}
              >
                Trust this file
              </Action>
            )}
          </ScrollView>
        </SafeAreaView>
      </Modal>
    </>
  );
}
