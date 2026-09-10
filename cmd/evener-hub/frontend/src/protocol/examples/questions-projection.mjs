import assert from "node:assert/strict";

const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value) => typeof value === "string" && value.trim().length > 0;

function parseCall(item) {
  assert.ok(text(item.id) && text(item.callId) && typeof item.argumentsJson === "string", "Invalid question identity.");
  let args;
  try {
    args = JSON.parse(item.argumentsJson);
  } catch {
    throw new Error("Invalid question arguments.");
  }
  assert.ok(
    record(args) && Array.isArray(args.questions) && args.questions.length >= 1 && args.questions.length <= 4,
    "Invalid question batch.",
  );
  const questions = args.questions.map((question, index) => {
    assert.ok(record(question) && typeof question.question === "string", "Invalid question.");
    for (const key of ["header", "why", "if_unanswered"])
      assert.ok(question[key] === undefined || typeof question[key] === "string", "Invalid question detail.");
    assert.ok(
      question.multi_select === undefined || typeof question.multi_select === "boolean",
      "Invalid selection mode.",
    );
    assert.ok(
      Array.isArray(question.options) && question.options.length >= 2 && question.options.length <= 5,
      "Invalid question options.",
    );
    const labels = new Set();
    let recommendations = 0;
    for (const option of question.options) {
      assert.ok(
        record(option) && typeof option.label === "string" && typeof option.detail === "string",
        "Invalid question option.",
      );
      assert.ok(!labels.has(option.label), "Duplicate option label.");
      labels.add(option.label);
      assert.ok(option.recommended === undefined || typeof option.recommended === "boolean", "Invalid recommendation.");
      if (option.recommended === true) recommendations += 1;
    }
    assert.ok(recommendations <= 1, "Multiple recommendations.");
    return { ...question, header: question.header ?? `Question ${index + 1}` };
  });
  return {
    id: item.id,
    callId: item.callId,
    transcriptKey: item.transcriptKey,
    position: item.position,
    argumentsJson: item.argumentsJson,
    questions,
  };
}

// The caller supplies a complete chronological window through the latest input.
// Each successful ask_user call after that boundary belongs to the same decision.
export function projectQuestions(items) {
  const latestUser = items.findLastIndex((item) => item.type === "userMessage");
  assert.ok(latestUser >= 0, "Latest user message unavailable; review a complete question batch.");
  const calls = [];
  for (const item of items.slice(latestUser + 1)) {
    if (item.type !== "commandExecution" || item.toolName !== "ask_user" || item.status !== "completed") continue;
    assert.ok(item.error === undefined || typeof item.error === "string", "Invalid question error state.");
    if (item.error === undefined || item.error === "") calls.push(parseCall(item));
  }
  return { calls, questions: calls.flatMap((call) => call.questions) };
}

// SDK writes require an authored resolution for every question. UI defaults are
// not consent to submit, and this recipe never fills missing answers.
export function validateQuestionSelections(questions, selections) {
  assert.ok(Array.isArray(selections) && selections.length === questions.length, "Answer the complete question batch.");
  return questions.map((question, index) => {
    const selection = selections[index];
    assert.ok(
      record(selection) &&
        typeof selection.note === "string" &&
        Object.keys(selection).every((key) => ["resolution", "note"].includes(key)),
      "Invalid question selection.",
    );
    const resolution = selection.resolution;
    assert.ok(record(resolution) && typeof resolution.kind === "string", "Provide an explicit question resolution.");
    const keys = {
      option: ["kind", "labels"],
      free: ["kind", "text"],
      decide: ["kind", "leaning"],
      fallback: ["kind"],
      skip: ["kind"],
    };
    assert.ok(
      Object.hasOwn(keys, resolution.kind) &&
        Object.keys(resolution).every((key) => keys[resolution.kind].includes(key)),
      "Invalid question resolution.",
    );
    switch (resolution.kind) {
      case "option":
        assert.ok(
          Array.isArray(resolution.labels) &&
            resolution.labels.length > 0 &&
            new Set(resolution.labels).size === resolution.labels.length &&
            (question.multi_select === true || resolution.labels.length === 1) &&
            resolution.labels.every((label) => question.options.some((option) => option.label === label)),
          "Invalid selected labels.",
        );
        break;
      case "free":
        assert.equal(typeof resolution.text, "string", "Invalid free response.");
        break;
      case "decide":
        assert.equal(typeof resolution.leaning, "string", "Invalid leaning.");
        break;
      case "fallback":
        assert.equal(typeof question.if_unanswered, "string", "Fallback unavailable.");
        break;
    }
    return { header: question.header, resolution, note: selection.note, ifUnanswered: question.if_unanswered };
  });
}
