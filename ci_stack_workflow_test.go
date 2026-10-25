package evener_test

import (
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCIIncludesNotificationRepairStack(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]map[string][]string `yaml:"on"`
		Permissions map[string]string              `yaml:"permissions"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string][]string{
		"push": {"branches": {"main"}},
		"pull_request": {"branches": {
			"main", "unified-session-notifications", "notification-repair-execution",
			"notification-repair-receiver", "notification-repair-stop",
		}},
	}
	if !reflect.DeepEqual(workflow.On, want) {
		t.Errorf("CI triggers = %#v, want %#v", workflow.On, want)
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("CI permissions = %#v, want contents read only", workflow.Permissions)
	}
}
