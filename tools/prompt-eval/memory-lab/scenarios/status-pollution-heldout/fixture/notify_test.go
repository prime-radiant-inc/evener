package notify

import "testing"

func TestValidate(t *testing.T) {
	if err := Validate(Message{To: "a@example.com", Body: "hi"}); err != nil {
		t.Fatalf("Validate = %v", err)
	}
	if err := Validate(Message{Body: "hi"}); err == nil {
		t.Fatal("Validate accepted a message with no recipient")
	}
}
