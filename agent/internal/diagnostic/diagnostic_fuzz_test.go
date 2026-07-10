//go:build serffuzz

package diagnostic

import (
	"errors"
	"testing"

	"primeradiant.com/serf/llm"
)

func FuzzDiagnosticClassification(f *testing.F) {
	f.Add("unknown provider: scripted", "provider", "", "", uint8(0))
	f.Add("rendezvous timed out", "hub", "custom title", "custom hint", uint8(1))
	f.Add("rate limit exceeded", "not-a-source", "", "", uint8(2))

	f.Fuzz(func(t *testing.T, message, source, title, hint string, errorKind uint8) {
		classified := Classify(message)
		assertFuzzDiagnosticInfo(t, "Classify", classified)
		if again := Classify(message); again != classified {
			t.Fatalf("Classify is not deterministic: %+v then %+v", classified, again)
		}

		var err error
		switch errorKind % 3 {
		case 0:
			err = errors.New(message)
		case 1:
			err = &llm.ConfigurationError{Message: message}
		case 2:
			err = llm.ErrorFromHTTPStatus("scripted", 500, message, nil, nil)
		}
		fromError := FromError(err)
		assertFuzzDiagnosticInfo(t, "FromError", fromError)
		if again := FromError(err); again != fromError {
			t.Fatalf("FromError is not deterministic: %+v then %+v", fromError, again)
		}

		fromFields := FromFields(source, title, hint, message)
		assertFuzzDiagnosticInfo(t, "FromFields", fromFields)
		if again := FromFields(source, title, hint, message); again != fromFields {
			t.Fatalf("FromFields is not deterministic: %+v then %+v", fromFields, again)
		}
	})
}

func assertFuzzDiagnosticInfo(t *testing.T, operation string, info Info) {
	t.Helper()
	switch info.Source {
	case SourceProvider, SourceSerf, SourceHub, SourceUI, SourceHook, SourceMCP:
	default:
		t.Fatalf("%s returned undeclared source %q in %+v", operation, info.Source, info)
	}
}
