package providercfg

import "testing"

func TestLoad_CacheRetentionAndAffinityFlags(t *testing.T) {
	data := []byte(`
default = "gw"

[instances.gw]
type = "openai"
api_style = "chat-completions"
base_url = "https://gw/v1"

[instances.gw.compat]
supports_long_cache_retention = true
send_session_affinity_headers = true
`)
	cfg, err := Load(data)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := cfg.Instances[0].Compat
	if c == nil {
		t.Fatalf("compat nil")
	}
	if c.SupportsLongCacheRetention == nil || !*c.SupportsLongCacheRetention {
		t.Errorf("SupportsLongCacheRetention = %v, want true", c.SupportsLongCacheRetention)
	}
	if c.SendSessionAffinityHeaders == nil || !*c.SendSessionAffinityHeaders {
		t.Errorf("SendSessionAffinityHeaders = %v, want true", c.SendSessionAffinityHeaders)
	}
}

func TestMarshal_CacheRetentionAndAffinityFlags_RoundTrip(t *testing.T) {
	tru := true
	cfg := Config{
		Default: "gw",
		Instances: []InstanceConfig{{
			Name:     "gw",
			Type:     "openai",
			APIStyle: StyleChatCompletions,
			Compat: &CompatConfig{
				SupportsLongCacheRetention: &tru,
				SendSessionAffinityHeaders: &tru,
			},
		}},
	}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Load(data)
	if err != nil {
		t.Fatalf("Load(Marshal): %v\n%s", err, data)
	}
	c := got.Instances[0].Compat
	if c == nil || c.SupportsLongCacheRetention == nil || !*c.SupportsLongCacheRetention {
		t.Fatalf("round-trip lost supports_long_cache_retention: %+v\n%s", c, data)
	}
	if c.SendSessionAffinityHeaders == nil || !*c.SendSessionAffinityHeaders {
		t.Fatalf("round-trip lost send_session_affinity_headers: %+v\n%s", c, data)
	}
}
