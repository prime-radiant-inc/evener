package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	apilog "primeradiant.com/serf/llm/apilog"
)

func FuzzCanonicalAPILoggerAppendPrograms(f *testing.F) {
	for mode := uint8(0); mode < 4; mode++ {
		f.Add(mode, []byte("session"))
	}
	f.Fuzz(func(t *testing.T, mode uint8, data []byte) {
		if len(data) > 64 {
			data = data[:64]
		}
		stateDir := t.TempDir()
		logger, err := NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithAPILogContext(context.Background(), "safe-session")
		switch mode % 4 {
		case 0:
			contexts := []context.Context{
				ctx,
				WithAPILogContext(context.Background(), "other-session"),
				WithAPILogContext(context.Background(), string(data)+"/unsafe"),
				context.Background(),
			}
			var wg sync.WaitGroup
			errs := make(chan error, len(contexts))
			for i, appendCtx := range contexts {
				wg.Add(1)
				go func(index int, appendCtx context.Context) {
					defer wg.Done()
					errs <- logger.AppendAttempt(appendCtx, standaloneCanonicalAttempt("ag_fuzz_route", index+1))
				}(i, appendCtx)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"safe-session.api.jsonl", "other-session.api.jsonl", "unattributed.api.jsonl"} {
				path := filepath.Join(stateDir, "sessions", name)
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				decoder := apilog.NewDecoder(file, 1<<20)
				if _, err := decoder.Next(); err != nil {
					t.Fatalf("decode %s: %v", name, err)
				}
				_ = file.Close()
			}
		case 1:
			oldMarshal := apiLogJSONMarshal
			apiLogJSONMarshal = func(any) ([]byte, error) { return nil, errors.New("scripted marshal failure") }
			err := logger.AppendAttempt(ctx, standaloneCanonicalAttempt("ag_fuzz_marshal", 1))
			apiLogJSONMarshal = oldMarshal
			if err == nil {
				t.Fatal("marshal failure was accepted")
			}
			_ = logger.Close()
		case 2:
			if err := logger.AppendAttempt(ctx, standaloneCanonicalAttempt("ag_fuzz_close", 1)); err != nil {
				t.Fatal(err)
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			if err := logger.AppendAttempt(ctx, standaloneCanonicalAttempt("ag_fuzz_close", 2)); err == nil {
				t.Fatal("append after close succeeded")
			}
		case 3:
			oldSync := apiLogFileSync
			apiLogFileSync = func(*os.File) error { return errors.New("scripted sync failure") }
			err := logger.AppendAttempt(ctx, standaloneCanonicalAttempt("ag_fuzz_sync", 1))
			apiLogFileSync = oldSync
			if err == nil {
				t.Fatal("sync failure was accepted")
			}
			_ = logger.Close()
		}
	})
}
