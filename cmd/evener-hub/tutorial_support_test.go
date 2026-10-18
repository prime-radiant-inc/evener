package hub

import (
	"encoding/json"
	"fmt"
	"os"
)

// tutorialProviderConfig keeps provider discovery inside the owned HTTP boundary.
func tutorialProviderConfig(baseURL string) string {
	return fmt.Sprintf("default = \"fake\"\n[providers.fake]\nbase = \"openai-compatible\"\nbase_url = %q\napi_key = \"fakellm-not-a-secret\"\n[providers.ollama]\nbase_url = %q\n[providers.ollama.models.\"*\"]\ndisabled = true\n", baseURL, baseURL)
}

func writeTutorialCommandRecords(path string, records []commandRecord) error {
	b, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
