import { useRef, useState } from "react";
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
import type { AskResolution } from "../../cmd/evener-hub/frontend/src/panes/session/composer/askDock/askCompose";
import type { MobileAskQuestion } from "../../mobile/src/conversation/model";
import type { DraftDestination } from "./draftRepository";
import { nativeDrafts } from "./nativeDrafts";
import {
  composeQuestionAnswers,
  nextUnansweredQuestion,
  type QuestionSelections,
  questionAdvanceTarget,
  seedQuestionAnswers,
} from "./questionAnswers";
import { Action, Copy, ErrorMessage, styles, useColors } from "./ui";
export function QuestionSheet({
  visible,
  destination,
  questions,
  hubName,
  ready,
  pending,
  error,
  close,
  send,
}: {
  visible: boolean;
  destination: DraftDestination;
  questions: MobileAskQuestion[];
  hubName: string;
  ready: boolean;
  pending: boolean;
  error: string | null;
  close: () => void;
  send: (selections: QuestionSelections) => Promise<void>;
}) {
  const colors = useColors();
  const signature = JSON.stringify(questions);
  function loadSelections() {
    try {
      const activeKey = nativeDrafts().readQuestionPosition(
        destination,
        questions.map((item) => item.key),
      );
      return {
        selections: seedQuestionAnswers(
          questions,
          nativeDrafts().readQuestions(destination, signature),
        ),
        activeIndex: Math.max(
          0,
          questions.findIndex((question) => question.key === activeKey),
        ),
        loaded: true,
        error: null as string | null,
      };
    } catch {
      return {
        activeIndex: 0,
        selections: {} as QuestionSelections,
        loaded: false,
        error: "Saved answers could not be loaded. Retry before editing.",
      };
    }
  }
  const [saved, setSaved] = useState(loadSelections);
  const selections = saved.selections;
  const activeIndex = saved.activeIndex;
  const [positionError, setPositionError] = useState<string | null>(null);
  function setActiveIndex(index: number) {
    const question = questions[index];
    if (!question || !saved.loaded) return;
    setSaved((values) => ({ ...values, activeIndex: index }));
    try {
      nativeDrafts().writeQuestionPosition(destination, question.key);
      setPositionError(null);
    } catch {
      setPositionError(
        "Your place could not be saved. Your answers are kept separately.",
      );
    }
  }
  const input = useRef<TextInput>(null);
  function setSelections(
    update: (values: QuestionSelections) => QuestionSelections,
  ) {
    if (!saved.loaded) return;
    const selections = update(saved.selections);
    try {
      nativeDrafts().writeQuestions(destination, signature, selections);
      setSaved((values) => ({
        ...values,
        selections,
        loaded: true,
        error: null,
      }));
    } catch {
      setSaved({
        activeIndex,
        selections,
        loaded: true,
        error:
          "These answers could not be saved. Keep this sheet open and retry saving.",
      });
    }
  }
  const editable = ready && saved.loaded && !pending;

  function select(key: string, resolution: AskResolution | null) {
    const next = {
      ...selections,
      [key]: { note: selections[key]?.note ?? "", resolution },
    };
    setSelections(() => next);
    if (
      !selections[key]?.resolution &&
      resolution?.kind === "option" &&
      !questions[activeIndex]?.multiSelect
    ) {
      const target = nextUnansweredQuestion(questions, next, activeIndex);
      if (target !== undefined) setActiveIndex(target);
    }
  }
  const text = composeQuestionAnswers(questions, selections);
  const advanceTarget = questionAdvanceTarget(
    questions,
    selections,
    activeIndex,
  );
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
            {positionError ? (
              <View>
                <ErrorMessage message={positionError} />
                <Action onPress={() => setActiveIndex(activeIndex)}>
                  Retry saving your place
                </Action>
              </View>
            ) : null}
            {saved.error ? (
              <View>
                <ErrorMessage message={saved.error} />
                <Action
                  onPress={() => {
                    if (saved.loaded) setSelections((values) => values);
                    else setSaved(loadSelections());
                  }}
                >
                  {`Retry ${saved.loaded ? "saving answers" : "loading answers"}`}
                </Action>
              </View>
            ) : null}
            {questions.length > 1 ? (
              <ScrollView horizontal showsHorizontalScrollIndicator={false}>
                {questions.map((question, index) => (
                  <Pressable
                    key={question.key}
                    accessibilityRole="tab"
                    accessibilityState={{ selected: index === activeIndex }}
                    onPress={() => setActiveIndex(index)}
                    style={{ padding: 12, minHeight: 48 }}
                  >
                    <Copy>{`${index + 1}. ${question.header}${selections[question.key]?.resolution ? " ✓" : ""}`}</Copy>
                  </Pressable>
                ))}
              </ScrollView>
            ) : null}
            {questions.slice(activeIndex, activeIndex + 1).map((question) => {
              const answer = selections[question.key];
              return (
                <View key={question.key} style={{ gap: 12 }}>
                  <Copy muted>{question.header}</Copy>
                  <Copy>{question.question}</Copy>
                  {question.why ? <Copy muted>{question.why}</Copy> : null}
                  {question.multiSelect ? (
                    <Copy muted>Choose any that apply.</Copy>
                  ) : null}
                  {[...question.options]
                    .sort(
                      (a, b) =>
                        Number(!!b.recommended) - Number(!!a.recommended),
                    )
                    .map((option) => {
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
                            disabled: !editable,
                          }}
                          disabled={!editable}
                          onPress={() => {
                            const labels =
                              question.multiSelect &&
                              answer?.resolution?.kind === "option"
                                ? answer.resolution.labels
                                : [];
                            const next =
                              question.multiSelect && checked
                                ? labels.filter(
                                    (label) => label !== option.label,
                                  )
                                : question.multiSelect
                                  ? [...labels, option.label]
                                  : [option.label];
                            select(
                              question.key,
                              next.length
                                ? { kind: "option", labels: next }
                                : null,
                            );
                          }}
                          style={{
                            padding: 12,
                            minHeight: 48,
                            borderWidth: 1,
                            borderRadius: 12,
                            borderColor: checked
                              ? colors.accent
                              : colors.border,
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
                  <Pressable
                    accessibilityRole="radio"
                    accessibilityLabel="Something else…"
                    accessibilityState={{
                      checked: answer?.resolution?.kind === "free",
                      disabled: !editable,
                    }}
                    disabled={!editable}
                    onPress={() => {
                      const free = answer?.resolution?.kind === "free";
                      select(
                        question.key,
                        free ? null : { kind: "free", text: "" },
                      );
                      if (!free) input.current?.focus();
                    }}
                    style={{ minHeight: 48, justifyContent: "center" }}
                  >
                    <Copy>{`${answer?.resolution?.kind === "free" ? "✓ " : ""}Something else…`}</Copy>
                  </Pressable>
                  <TextInput
                    ref={input}
                    accessibilityLabel={`${answer?.resolution?.kind === "free" ? "Answer" : "Note"} for ${question.header}`}
                    placeholder={
                      answer?.resolution?.kind === "free"
                        ? "Type your answer"
                        : "Note (optional)"
                    }
                    placeholderTextColor={colors.secondary}
                    multiline
                    editable={editable}
                    value={
                      answer?.resolution?.kind === "free"
                        ? answer.resolution.text
                        : (answer?.note ?? "")
                    }
                    onChangeText={(value) => {
                      if (answer?.resolution?.kind === "free")
                        select(question.key, { kind: "free", text: value });
                      else
                        setSelections((values) => ({
                          ...values,
                          [question.key]: {
                            resolution:
                              values[question.key]?.resolution ?? null,
                            note: value,
                          },
                        }));
                    }}
                    style={[
                      styles.input,
                      { color: colors.text, borderColor: colors.border },
                    ]}
                  />
                </View>
              );
            })}
            <Copy muted>
              {`${questions.filter((question) => selections[question.key]?.resolution).length} of ${questions.length} answered`}
            </Copy>
            <Action
              tone="primary"
              disabled={
                !editable ||
                !!saved.error ||
                (advanceTarget === undefined && text === null)
              }
              onPress={() => {
                if (advanceTarget !== undefined) setActiveIndex(advanceTarget);
                else void send(selections);
              }}
            >
              {pending
                ? "Sending answers…"
                : advanceTarget !== undefined
                  ? "Next question"
                  : "Send answers"}
            </Action>
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    </Modal>
  );
}
