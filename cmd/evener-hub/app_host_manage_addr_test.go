package hub

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// Runtime host adapters share bootstrap-listener validation with TOML loading.

func TestHostManageAddRefusesUnusableHostAddr(t *testing.T) {
	m := testHostManager(nil, nil)
	for _, tc := range []struct {
		entry appwire.HostEntry
		field string
	}{
		{appwire.HostEntry{Name: "noport", Address: "n.example", ConfigPath: "/etc/evener/hub.toml", Addr: "127.0.0.1"}, "addr"},
	} {
		_, err := m.Add(context.Background(), appwire.HostAddParams{Entry: tc.entry})
		if err == nil {
			t.Errorf("Add(%+v) accepted an entry hub.toml loading refuses", tc.entry)
			continue
		}
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Errorf("Add(%+v) error = %v, want InvalidParams", tc.entry, err)
			continue
		}
		data, ok := wire.Data.(appwire.HostFieldErrorData)
		if tc.field == "" {
			if ok {
				t.Errorf("Add(%+v) blamed field %q, want the entry as a whole", tc.entry, data.Field)
			}
			continue
		}
		if !ok || data.Field != tc.field {
			t.Errorf("Add(%+v) data = %#v, want field %q", tc.entry, wire.Data, tc.field)
		}
	}
	if got := m.cfg.hosts.All(); len(got) != 0 {
		t.Fatalf("failed adds committed %d hosts, want none: %+v", len(got), got)
	}
}

func TestHostManageUpdateRefusesUnusableHostAddr(t *testing.T) {
	f := newUpdateFixture(t)
	before := readHubTOMLBytes(t, f.configPath)
	for _, entry := range []appwire.HostEntry{
		{Address: "side.example", ConfigPath: "/etc/evener/hub.toml", Addr: "10.0.0.5:70000"},
	} {
		if _, err := f.m.Update(context.Background(), updateRequest(t, f.m, "side", entry)); err == nil {
			t.Errorf("Update(%+v) accepted an entry hub.toml loading refuses", entry)
		}
	}
	if after := readHubTOMLBytes(t, f.configPath); !bytes.Equal(before, after) {
		t.Fatalf("a refused update changed hub.toml:\nbefore=%s\nafter=%s", before, after)
	}
	if got, ok := f.m.cfg.hosts.Get("side"); !ok || got.ConfigPath != "" || got.Addr != "" {
		t.Fatalf("refused updates changed the stored entry: %+v (ok=%v)", got, ok)
	}
}

func TestHostManageConfigOnlyAndBootstrapAddr(t *testing.T) {
	m := testHostManager(nil, nil)
	for _, entry := range []appwire.HostEntry{
		{Name: "cfgonly", Address: "c.example", ConfigPath: "/etc/evener/hub.toml"},
		{Name: "addronly", Address: "a.example", Addr: "0.0.0.0:0"},
		{Name: "specific", Address: "s.example", Addr: "10.0.0.5:9180"},
	} {
		if _, err := m.Add(t.Context(), appwire.HostAddParams{Entry: entry}); err != nil {
			t.Fatal(err)
		}
		got, ok := m.cfg.hosts.Get(entry.Name)
		if !ok || got.ConfigPath != entry.ConfigPath || got.Addr != entry.Addr {
			t.Fatalf("stored=%+v", got)
		}
	}
}
