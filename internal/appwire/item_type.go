package appwire

import "encoding/json"

type threadItemAlias ThreadItem

func (item ThreadItem) MarshalJSON() ([]byte, error) {
	alias := threadItemAlias(item)
	alias.Type = CodexItemType(item.Type)
	return json.Marshal(alias)
}

func (item *ThreadItem) UnmarshalJSON(data []byte) error {
	var alias threadItemAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*item = ThreadItem(alias)
	item.Type = InternalItemType(item.Type)
	return nil
}

func CodexItemType(itemType string) string {
	switch itemType {
	case "user_message":
		return "userMessage"
	case "agent_message":
		return "agentMessage"
	case "tool_call":
		return "commandExecution"
	default:
		return itemType
	}
}

func InternalItemType(itemType string) string {
	switch itemType {
	case "userMessage":
		return "user_message"
	case "agentMessage":
		return "agent_message"
	case "commandExecution", "mcpToolCall", "dynamicToolCall", "collabToolCall":
		return "tool_call"
	default:
		return itemType
	}
}
