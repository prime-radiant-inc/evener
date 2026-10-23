```json
{
  "model": "gpt-4.1-nano",
  "provider": "openai",
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "kind": "text",
          "text": "Extract the key facts from this coding agent action. Prefer a single line (under 100 tokens) listing concrete facts: file paths modified, values discovered, test results, decisions made, errors encountered. Prefer factual fragments, not prose. These size and style preferences must yield to the authority-quotation rule below.\n\nQuote every permission, approval, hold or stop your human partner gave, word for word from their \"User:\" message, and say which of their messages it came from. Carry forward only what the conversation contains: never add one it lacks, never turn a question, a suggestion or your own caution into one, and never drop one that is still in force. An earlier compaction counts only for what it quotes. If there are none, say \"None.\"\n\nRecent action:\nUser: Where is the request parser?\nAssistant: I would review the diff before making edits.\n\nKey facts:"
        }
      ]
    }
  ],
  "max_tokens": 32768,
  "adapter_timeout": {
    "connect": 10000000000,
    "request": 0,
    "stream_read": 600000000000
  }
}
```
