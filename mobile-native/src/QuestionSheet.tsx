import { useState } from "react";
import {
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  TextInput,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import type { AskResolution } from "../../mobile/src/components/composer/composeAskAnswers";
import type { MobileAskQuestion } from "../../mobile/src/conversation/model";
import {
  composeQuestionAnswers,
  type QuestionSelections,
} from "./questionAnswers";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";
export function QuestionSheet({
  visible,
  questions,
  hubName,
  ready,
  pending,
  error,
  close,
  send,
}: {
  visible: boolean;
  questions: MobileAskQuestion[];
  hubName: string;
  ready: boolean;
  pending: boolean;
  error: string | null;
  close: () => void;
  send: (selections: QuestionSelections) => Promise<void>;
}) {
  const colors = useColors();
  const [selections, setSelections] = useState<QuestionSelections>({});
  function select(key: string, resolution: AskResolution) {
    setSelections((values) => ({
      ...values,
      [key]: { note: values[key]?.note ?? "", resolution },
    }));
  }
  const text = composeQuestionAnswers(questions, selections);
  return (
    <Modal
      visible={visible}
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
            <Copy>Questions to answer</Copy>
            <Copy muted>{hubName}</Copy>
          </View>
          <Action onPress={close}>Done</Action>
        </View>
        <KeyboardAvoidingView
          style={styles.fill}
          behavior={Platform.OS === "ios" ? "padding" : "height"}
        >
          <ScrollView
            keyboardShouldPersistTaps="handled"
            contentContainerStyle={{ padding: 20, gap: 24 }}
          >
            {error ? <ErrorMessage message={error} /> : null}
            {questions.map((question) => {
              const answer = selections[question.key];
              return (
                <View key={question.key} style={{ gap: 12 }}>
                  <Copy muted>{question.header}</Copy>
                  <Copy>{question.question}</Copy>
                  {question.why ? <Copy muted>{question.why}</Copy> : null}
                  {question.multiSelect ? (
                    <Copy muted>Choose any that apply.</Copy>
                  ) : null}
                  {question.options.map((option) => {
                    const checked =
                      answer?.resolution?.kind === "option" &&
                      answer.resolution.labels.includes(option.label);
                    return (
                      <Pressable
                        key={option.label}
                        accessibilityRole={
                          question.multiSelect ? "checkbox" : "radio"
                        }
                        accessibilityState={{
                          checked,
                          disabled: !ready || pending,
                        }}
                        disabled={!ready || pending}
                        onPress={() => {
                          const labels =
                            question.multiSelect &&
                            answer?.resolution?.kind === "option"
                              ? answer.resolution.labels
                              : [];
                          select(question.key, {
                            kind: "option",
                            labels: checked
                              ? labels.filter((label) => label !== option.label)
                              : [...labels, option.label],
                          });
                        }}
                        style={{
                          padding: 12,
                          minHeight: 48,
                          borderWidth: 1,
                          borderRadius: 12,
                          borderColor: checked ? colors.accent : colors.border,
                          backgroundColor: colors.surface,
                        }}
                      >
                        <Copy>
                          {checked ? "✓ " : ""}
                          {option.label}
                          {option.recommended ? " · Recommended" : ""}
                        </Copy>
                        {option.detail ? (
                          <Copy muted>{option.detail}</Copy>
                        ) : null}
                      </Pressable>
                    );
                  })}
                  <TextInput
                    accessibilityLabel={`Written answer for ${question.header}`}
                    placeholder="Or write your answer"
                    placeholderTextColor={colors.secondary}
                    multiline
                    editable={ready && !pending}
                    value={
                      answer?.resolution?.kind === "free"
                        ? answer.resolution.text
                        : ""
                    }
                    onChangeText={(text) =>
                      select(question.key, { kind: "free", text })
                    }
                    style={[
                      styles.input,
                      { color: colors.text, borderColor: colors.border },
                    ]}
                  />
                  <View style={[styles.row, { flexWrap: "wrap" }]}>
                    {(
                      [
                        { kind: "decide", leaning: "" },
                        { kind: "skip" },
                        ...(question.ifUnanswered
                          ? [{ kind: "fallback" }]
                          : []),
                      ] as AskResolution[]
                    ).map((resolution) => (
                      <Action
                        key={resolution.kind}
                        disabled={!ready || pending}
                        onPress={() => select(question.key, resolution)}
                      >{`${answer?.resolution?.kind === resolution.kind ? "✓ " : ""}${resolution.kind === "decide" ? "You decide" : resolution.kind === "skip" ? "Skip" : "Use fallback"}`}</Action>
                    ))}
                  </View>
                  {answer?.resolution?.kind === "decide" ? (
                    <TextInput
                      accessibilityLabel={`Leaning for ${question.header}`}
                      placeholder="Optional leaning"
                      placeholderTextColor={colors.secondary}
                      editable={ready && !pending}
                      value={answer.resolution.leaning}
                      onChangeText={(leaning) =>
                        select(question.key, { kind: "decide", leaning })
                      }
                      style={[
                        styles.input,
                        { color: colors.text, borderColor: colors.border },
                      ]}
                    />
                  ) : null}
                  {question.ifUnanswered ? (
                    <Copy muted>Fallback: {question.ifUnanswered}</Copy>
                  ) : null}
                  <TextInput
                    accessibilityLabel={`Note for ${question.header}`}
                    placeholder="Optional note"
                    placeholderTextColor={colors.secondary}
                    multiline
                    editable={ready && !pending}
                    value={answer?.note ?? ""}
                    onChangeText={(note) =>
                      setSelections((values) => ({
                        ...values,
                        [question.key]: {
                          resolution: values[question.key]?.resolution ?? null,
                          note,
                        },
                      }))
                    }
                    style={[
                      styles.input,
                      { color: colors.text, borderColor: colors.border },
                    ]}
                  />
                </View>
              );
            })}
            <Copy muted>
              Choose an answer or explicitly skip each question before sending.
            </Copy>
            <Action
              tone="primary"
              disabled={!ready || pending || text === null}
              onPress={() => {
                void send(selections);
              }}
            >
              {pending ? "Sending answers…" : "Send answers"}
            </Action>
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
