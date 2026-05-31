package providercfg

import (
	"fmt"
	"strings"
)

// Marshal emits providers.toml content for cfg. It never emits api_key even
// if InstanceConfig.APIKey is set. The output round-trips through Load.
func Marshal(cfg Config) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "default = %q\n", cfg.Default)
	for _, inst := range cfg.Instances {
		fmt.Fprintf(&b, "\n[instances.%s]\n", inst.Name)
		fmt.Fprintf(&b, "type = %q\n", inst.Type)
		if inst.APIStyle != "" {
			fmt.Fprintf(&b, "api_style = %q\n", inst.APIStyle)
		}
		if inst.BaseURL != "" {
			fmt.Fprintf(&b, "base_url = %q\n", inst.BaseURL)
		}
		if inst.Quirks != "" {
			fmt.Fprintf(&b, "quirks = %q\n", inst.Quirks)
		}
	}
	return []byte(b.String()), nil
}
