import type { DiagnosticSink, PrototypeFixture, SessionState } from "./model";

type RecordValue = Record<string, unknown>;
type ValidationResult = string | null;

const sessionStates: readonly SessionState[] = [
  "needs-answer",
  "needs-permission",
  "running",
  "waiting",
  "completed",
  "failed",
];

function isRecord(value: unknown): value is RecordValue {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function inspectPlainRecord(value: unknown, path: string): ValidationResult {
  if (!isRecord(value)) return path;
  const prototype = Object.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) return path;
  const keys = Reflect.ownKeys(value);
  const descriptors = Object.getOwnPropertyDescriptors(
    value,
  ) as unknown as Record<PropertyKey, PropertyDescriptor>;
  for (const key of keys) {
    if (typeof key !== "string") return path;
    const descriptor = descriptors[key];
    if (!descriptor) return path;
    if (
      !descriptor.enumerable ||
      !("value" in descriptor) ||
      descriptor.get !== undefined ||
      descriptor.set !== undefined
    ) {
      return path;
    }
  }
  return null;
}

function inspectPlainArray(value: unknown, path: string): ValidationResult {
  if (!Array.isArray(value)) return path;
  if (Object.getPrototypeOf(value) !== Array.prototype) return path;
  const keys = Reflect.ownKeys(value);
  const descriptors = Object.getOwnPropertyDescriptors(
    value,
  ) as unknown as Record<PropertyKey, PropertyDescriptor>;
  const lengthDescriptor = descriptors.length;
  if (
    !lengthDescriptor ||
    lengthDescriptor.enumerable ||
    !("value" in lengthDescriptor) ||
    !Number.isSafeInteger(lengthDescriptor.value) ||
    lengthDescriptor.value < 0
  ) {
    return path;
  }
  const length = lengthDescriptor.value as number;
  if (keys.length !== length + 1) return path;
  for (const key of keys) {
    if (typeof key !== "string") return path;
    if (key === "length") continue;
    const index = Number(key);
    if (!Number.isSafeInteger(index) || index < 0 || index >= length)
      return path;
    if (String(index) !== key) return path;
    const descriptor = descriptors[key];
    if (!descriptor) return path;
    if (
      !descriptor.enumerable ||
      !("value" in descriptor) ||
      descriptor.get !== undefined ||
      descriptor.set !== undefined
    ) {
      return path;
    }
  }
  for (let index = 0; index < length; index += 1) {
    if (!Object.hasOwn(descriptors, String(index))) return path;
  }
  return null;
}

function safeString(value: unknown, path: string): ValidationResult {
  if (typeof value !== "string") return path;
  if (/(?:https?:)?\/\//i.test(value)) return path;
  if (/^(?:~\/|\/Users\/|\/home\/)/.test(value)) return path;
  if (/^(?:bearer|basic)\s+/i.test(value)) return path;
  return null;
}

function booleanValue(value: unknown, path: string): ValidationResult {
  return typeof value === "boolean" ? null : path;
}

function finiteNumber(value: unknown, path: string): ValidationResult {
  return typeof value === "number" && Number.isFinite(value) ? null : path;
}

function oneOf(
  value: unknown,
  values: readonly string[],
  path: string,
): ValidationResult {
  return typeof value === "string" && values.includes(value) ? null : path;
}

function exactRecord(
  value: unknown,
  keys: readonly string[],
  path: string,
): ValidationResult {
  const shape = inspectPlainRecord(value, path);
  if (shape) return shape;
  if (!isRecord(value)) return path;
  const allowed = new Set(keys);
  const ownKeys = Reflect.ownKeys(value) as string[];
  for (const key of ownKeys) {
    if (!allowed.has(key)) return path;
  }
  const present = new Set(ownKeys);
  for (const key of keys) {
    if (!present.has(key)) return `${path}.${key}`;
  }
  return null;
}

function validateArray(
  value: unknown,
  path: string,
  validator: (item: unknown, path: string) => ValidationResult,
): ValidationResult {
  const shape = inspectPlainArray(value, path);
  if (shape) return shape;
  if (!Array.isArray(value)) return path;
  for (const [index, item] of value.entries()) {
    const invalid = validator(item, `${path}[${index}]`);
    if (invalid) return invalid;
  }
  return null;
}

function validateFields(
  value: RecordValue,
  path: string,
  fields: ReadonlyArray<
    readonly [string, (value: unknown, path: string) => ValidationResult]
  >,
): ValidationResult {
  for (const [key, validator] of fields) {
    const invalid = validator(value[key], `${path}.${key}`);
    if (invalid) return invalid;
  }
  return null;
}

const stringField = (value: unknown, path: string) => safeString(value, path);

function validateSession(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(
    value,
    ["id", "title", "project", "state", "summary", "updatedLabel"],
    path,
  );
  if (invalid || !isRecord(value)) return invalid;
  return validateFields(value, path, [
    ["id", stringField],
    ["title", stringField],
    ["project", stringField],
    ["state", (item, itemPath) => oneOf(item, sessionStates, itemPath)],
    ["summary", stringField],
    ["updatedLabel", stringField],
  ]);
}

function validateTranscript(value: unknown, path: string): ValidationResult {
  if (!isRecord(value)) return path;
  const kind = value.kind;
  const common = ["id", "sessionId", "kind"];
  const variants: Record<string, readonly string[]> = {
    user: [...common, "body"],
    assistant: [...common, "body"],
    tool: [...common, "label", "status", "arguments", "output"],
    question: [...common, "questionId"],
    error: [...common, "title", "detail"],
    attachment: [...common, "name", "mediaType", "description"],
  };
  if (typeof kind !== "string" || !Object.hasOwn(variants, kind)) {
    return `${path}.kind`;
  }
  const variantKeys = variants[kind];
  if (!variantKeys) return `${path}.kind`;
  const invalid = exactRecord(value, variantKeys, path);
  if (invalid) return invalid;
  const commonInvalid = validateFields(value, path, [
    ["id", stringField],
    ["sessionId", stringField],
  ]);
  if (commonInvalid) return commonInvalid;
  if (kind === "user" || kind === "assistant") {
    return safeString(value.body, `${path}.body`);
  }
  if (kind === "tool") {
    return validateFields(value, path, [
      ["label", stringField],
      ["status", (item, itemPath) => oneOf(item, sessionStates, itemPath)],
      ["arguments", stringField],
      ["output", stringField],
    ]);
  }
  if (kind === "question") {
    return safeString(value.questionId, `${path}.questionId`);
  }
  if (kind === "error") {
    return validateFields(value, path, [
      ["title", stringField],
      ["detail", stringField],
    ]);
  }
  return validateFields(value, path, [
    ["name", stringField],
    ["mediaType", stringField],
    ["description", stringField],
  ]);
}

function validateQuestionOption(
  value: unknown,
  path: string,
): ValidationResult {
  const invalid = exactRecord(
    value,
    ["id", "label", "detail", "recommended"],
    path,
  );
  if (invalid || !isRecord(value)) return invalid;
  return validateFields(value, path, [
    ["id", stringField],
    ["label", stringField],
    ["detail", stringField],
    ["recommended", booleanValue],
  ]);
}

function validateQuestion(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(
    value,
    [
      "id",
      "prompt",
      "mode",
      "options",
      "allowNote",
      "allowFallback",
      "allowDecide",
      "allowSkip",
    ],
    path,
  );
  if (invalid || !isRecord(value)) return invalid;
  return validateFields(value, path, [
    ["id", stringField],
    ["prompt", stringField],
    ["mode", (item, itemPath) => oneOf(item, ["single", "multiple"], itemPath)],
    [
      "options",
      (item, itemPath) => validateArray(item, itemPath, validateQuestionOption),
    ],
    ["allowNote", booleanValue],
    ["allowFallback", booleanValue],
    ["allowDecide", booleanValue],
    ["allowSkip", booleanValue],
  ]);
}

function validateWork(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(
    value,
    [
      "id",
      "sessionId",
      "parentId",
      "kind",
      "title",
      "state",
      "phase",
      "elapsedLabel",
      "output",
    ],
    path,
  );
  if (invalid || !isRecord(value)) return invalid;
  return validateFields(value, path, [
    ["id", stringField],
    ["sessionId", stringField],
    [
      "parentId",
      (item, itemPath) => (item === null ? null : safeString(item, itemPath)),
    ],
    [
      "kind",
      (item, itemPath) => oneOf(item, ["task", "subagent", "job"], itemPath),
    ],
    ["title", stringField],
    ["state", (item, itemPath) => oneOf(item, sessionStates, itemPath)],
    ["phase", stringField],
    ["elapsedLabel", stringField],
    ["output", stringField],
  ]);
}

function validateSearch(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(
    value,
    ["id", "sessionId", "itemId", "kind", "title", "body"],
    path,
  );
  if (invalid || !isRecord(value)) return invalid;
  return validateFields(value, path, [
    ["id", stringField],
    ["sessionId", stringField],
    [
      "itemId",
      (item, itemPath) => (item === null ? null : safeString(item, itemPath)),
    ],
    [
      "kind",
      (item, itemPath) =>
        oneOf(
          item,
          ["session", "project", "transcript", "tool", "task"],
          itemPath,
        ),
    ],
    ["title", stringField],
    ["body", stringField],
  ]);
}

function validateProject(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(value, ["id", "label", "path"], path);
  if (invalid || !isRecord(value)) return invalid;
  const fields = validateFields(value, path, [
    ["id", stringField],
    ["label", stringField],
    ["path", stringField],
  ]);
  if (fields) return fields;
  return /^\/workspace\/(?:aurora|harbor)(?:\/|$)/.test(String(value.path))
    ? null
    : `${path}.path`;
}

function validateModel(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(
    value,
    ["id", "provider", "model", "label"],
    path,
  );
  if (invalid || !isRecord(value)) return invalid;
  return validateFields(value, path, [
    ["id", stringField],
    ["provider", stringField],
    ["model", stringField],
    ["label", stringField],
  ]);
}

function validateVoice(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(value, ["id", "state", "caption", "level"], path);
  if (invalid || !isRecord(value)) return invalid;
  const fields = validateFields(value, path, [
    ["id", stringField],
    [
      "state",
      (item, itemPath) =>
        oneOf(
          item,
          [
            "idle",
            "ready",
            "listening",
            "processing",
            "speaking",
            "interrupted",
            "denied",
            "error",
          ],
          itemPath,
        ),
    ],
    ["caption", stringField],
    ["level", finiteNumber],
  ]);
  if (fields) return fields;
  return Number(value.level) >= 0 && Number(value.level) <= 100
    ? null
    : `${path}.level`;
}

function validateHub(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(value, ["id", "name", "context", "state"], path);
  if (invalid || !isRecord(value)) return invalid;
  return validateFields(value, path, [
    ["id", stringField],
    ["name", stringField],
    ["context", stringField],
    [
      "state",
      (item, itemPath) => oneOf(item, ["connected", "offline"], itemPath),
    ],
  ]);
}

function validateUsage(value: unknown, path: string): ValidationResult {
  const invalid = exactRecord(
    value,
    ["tokens", "costLabel", "durationLabel", "contextPercent"],
    path,
  );
  if (invalid || !isRecord(value)) return invalid;
  const fields = validateFields(value, path, [
    ["tokens", finiteNumber],
    ["costLabel", stringField],
    ["durationLabel", stringField],
    ["contextPercent", finiteNumber],
  ]);
  if (fields) return fields;
  if (!Number.isInteger(value.tokens) || Number(value.tokens) < 0)
    return `${path}.tokens`;
  return Number(value.contextPercent) >= 0 &&
    Number(value.contextPercent) <= 100
    ? null
    : `${path}.contextPercent`;
}

function duplicatePath(collection: unknown[], path: string): ValidationResult {
  const seen = new Set<string>();
  for (const [index, item] of collection.entries()) {
    const id = (item as RecordValue).id as string;
    if (seen.has(id)) return `${path}[${index}].id`;
    seen.add(id);
  }
  return null;
}

function validateReferences(value: RecordValue): ValidationResult {
  const sessions = value.sessions as RecordValue[];
  const transcript = value.transcript as RecordValue[];
  const questions = value.questions as RecordValue[];
  const work = value.work as RecordValue[];
  const search = value.search as RecordValue[];
  const sessionIds = new Set(sessions.map(({ id }) => id));
  const transcriptIds = new Set(transcript.map(({ id }) => id));
  const questionIds = new Set(questions.map(({ id }) => id));
  const workIds = new Set(work.map(({ id }) => id));

  for (const [index, item] of transcript.entries()) {
    if (!sessionIds.has(item.sessionId))
      return `$.transcript[${index}].sessionId`;
    if (item.kind === "question" && !questionIds.has(item.questionId)) {
      return `$.transcript[${index}].questionId`;
    }
  }
  for (const [index, node] of work.entries()) {
    if (!sessionIds.has(node.sessionId)) return `$.work[${index}].sessionId`;
    if (node.parentId !== null && !workIds.has(node.parentId)) {
      return `$.work[${index}].parentId`;
    }
    const ancestry = new Set([node.id]);
    let parentId = node.parentId;
    while (parentId !== null) {
      if (ancestry.has(parentId)) return `$.work[${index}].parentId`;
      ancestry.add(parentId);
      parentId = work.find(({ id }) => id === parentId)?.parentId ?? null;
    }
  }
  for (const [index, document] of search.entries()) {
    if (!sessionIds.has(document.sessionId))
      return `$.search[${index}].sessionId`;
    if (document.itemId !== null && !transcriptIds.has(document.itemId)) {
      return `$.search[${index}].itemId`;
    }
  }
  for (const [index, question] of questions.entries()) {
    const optionInvalid = duplicatePath(
      question.options as unknown[],
      `$.questions[${index}].options`,
    );
    if (optionInvalid) return optionInvalid;
  }
  return null;
}

function validateFixture(value: RecordValue): ValidationResult {
  const keys = [
    "version",
    "sessions",
    "transcript",
    "questions",
    "work",
    "search",
    "recentProjects",
    "models",
    "efforts",
    "voiceSteps",
    "hubs",
    "usage",
  ];
  const exact = exactRecord(value, keys, "$");
  if (exact) return exact;
  const fields = validateFields(value, "$", [
    ["sessions", (item, path) => validateArray(item, path, validateSession)],
    [
      "transcript",
      (item, path) => validateArray(item, path, validateTranscript),
    ],
    ["questions", (item, path) => validateArray(item, path, validateQuestion)],
    ["work", (item, path) => validateArray(item, path, validateWork)],
    ["search", (item, path) => validateArray(item, path, validateSearch)],
    [
      "recentProjects",
      (item, path) => validateArray(item, path, validateProject),
    ],
    ["models", (item, path) => validateArray(item, path, validateModel)],
    [
      "efforts",
      (item, path) =>
        validateArray(item, path, (effort, effortPath) =>
          oneOf(effort, ["low", "medium", "high"], effortPath),
        ),
    ],
    ["voiceSteps", (item, path) => validateArray(item, path, validateVoice)],
    ["hubs", (item, path) => validateArray(item, path, validateHub)],
    ["usage", validateUsage],
  ]);
  if (fields) return fields;
  for (const collection of [
    "sessions",
    "transcript",
    "questions",
    "work",
    "search",
    "recentProjects",
    "models",
    "voiceSteps",
    "hubs",
  ]) {
    const duplicate = duplicatePath(
      value[collection] as unknown[],
      `$.${collection}`,
    );
    if (duplicate) return duplicate;
  }
  return validateReferences(value);
}

export function decodeFixture(
  value: unknown,
  fallback: PrototypeFixture,
  diagnostics: DiagnosticSink,
): PrototypeFixture {
  let diagnostic: { code: "fixture-invalid" | "fixture-version"; path: string };
  try {
    if (!isRecord(value)) {
      diagnostic = { code: "fixture-invalid", path: "$" };
    } else {
      const rootShape = inspectPlainRecord(value, "$");
      if (rootShape) {
        diagnostic = { code: "fixture-invalid", path: rootShape };
      } else {
        const versionDescriptor = Object.getOwnPropertyDescriptor(
          value,
          "version",
        );
        if (!versionDescriptor) {
          diagnostic = { code: "fixture-invalid", path: "$.version" };
        } else if (versionDescriptor.value !== 1) {
          diagnostic = { code: "fixture-version", path: "$.version" };
        } else {
          const invalid = validateFixture(value);
          if (!invalid) return value as unknown as PrototypeFixture;
          diagnostic = { code: "fixture-invalid", path: invalid };
        }
      }
    }
  } catch {
    diagnostic = { code: "fixture-invalid", path: "$" };
  }
  diagnostics.report(diagnostic);
  return fallback;
}
