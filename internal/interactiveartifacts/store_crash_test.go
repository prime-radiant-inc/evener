package interactiveartifacts

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

type crashRequest struct {
	Path, Stage, Operation string
	Raw                    []byte
}

// The helper runs the production store in a separate OS process. The barrier
// reports the exact transaction boundary and waits on a private pipe; only its
// owning parent kills it, without a sleep or a simulated SQLite implementation.
func TestStoreCrashChild(t *testing.T) {
	if !slices.Contains(os.Args, "artifact-crash-child") {
		t.Skip("owned subprocess helper")
	}
	var config crashRequest
	requireNoError(t, json.NewDecoder(os.Stdin).Decode(&config))
	hook := func() {
		_, err := fmt.Fprintln(os.Stdout, config.Stage)
		requireNoError(t, err)
		var release [1]byte
		_, err = io.ReadFull(os.Stdin, release[:])
		requireNoError(t, err)
	}
	options := StoreOptions{Clock: fixedClock}
	if config.Stage == "beforeCommit" {
		options.hooks.beforeCommit = hook
	} else {
		options.hooks.afterCommit = hook
	}
	s, err := OpenStore(config.Path, options)
	requireNoError(t, err)
	defer s.Close()
	hash := sha256.Sum256([]byte("child grant"))
	requireNoError(t, s.InstallGrant(context.Background(), hash, testScope()))
	if config.Operation == "create" {
		_, err = s.Publish(context.Background(), hash, config.Raw)
	} else {
		_, err = s.SaveState(context.Background(), hash, config.Raw)
	}
	requireNoError(t, err)
}

func killAtBarrier(t *testing.T, config crashRequest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreCrashChild$", "--", "artifact-crash-child")
	input, err := cmd.StdinPipe()
	requireNoError(t, err)
	defer input.Close()
	output, err := cmd.StdoutPipe()
	requireNoError(t, err)
	defer output.Close()
	cmd.Stderr = os.Stderr
	requireNoError(t, cmd.Start())
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	requireNoError(t, json.NewEncoder(input).Encode(config))
	signal := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		if scanner.Scan() {
			signal <- scanner.Text()
		} else {
			signal <- "child exited before barrier"
		}
	}()
	select {
	case stage := <-signal:
		if stage != config.Stage {
			t.Fatalf("wanted %s barrier, got %s", config.Stage, stage)
		}
	case <-ctx.Done():
		t.Fatal("child did not reach durable boundary")
	}
	requireNoError(t, cmd.Process.Kill())
	err = cmd.Wait()
	waited = true
	if err == nil {
		t.Fatal("owned child unexpectedly exited successfully")
	}
	t.Logf("owned child reached %s then was killed before reply", config.Stage)
}

func TestStoreProcessCrashRecovery(t *testing.T) {
	for _, operation := range []string{"create", "save"} {
		for _, stage := range []string{"beforeCommit", "afterCommit"} {
			t.Run(operation+"/"+stage, func(t *testing.T) {
				s, hash, path := setupStore(t, StoreOptions{})
				ctx := context.Background()
				identity := s.ServiceID()
				raw := createJSON("creation in child")
				var artifact string
				if operation == "save" {
					created, err := s.Publish(ctx, hash, createJSON("create"))
					requireNoError(t, err)
					artifact = created.ArtifactID
					raw = saveJSON(artifact, "save in child", 1, 1, `{"selection":"a"}`)
				}
				requireNoError(t, s.Close())
				killAtBarrier(t, crashRequest{Path: path, Stage: stage, Operation: operation, Raw: raw})
				s = openTestStore(t, path, StoreOptions{Clock: fixedClock})
				if s.ServiceID() != identity {
					t.Fatal("crash lost service identity")
				}
				requireNoError(t, s.InstallGrant(ctx, hash, testScope()))
				if operation == "save" {
					want := Version(1)
					if stage == "afterCommit" {
						want = 2
					}
					if got := readState(t, s, hash, artifact); got.StateVersion != want {
						t.Fatalf("boundary %s left state version %d", stage, got.StateVersion)
					}
				}
				var receiptCount int
				requireNoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM artifact_mutations WHERE mutation_id IN (?,?)", "creation in child", "save in child").Scan(&receiptCount))
				want := 0
				if stage == "afterCommit" {
					want = 1
				}
				if receiptCount != want {
					t.Fatalf("boundary %s retained %d receipts", stage, receiptCount)
				}
				var first, again MutationReceipt
				var err error
				if operation == "create" {
					first, err = s.Publish(ctx, hash, raw)
				} else {
					first, err = s.SaveState(ctx, hash, raw)
				}
				requireNoError(t, err)
				if operation == "create" {
					again, err = s.Publish(ctx, hash, raw)
				} else {
					again, err = s.SaveState(ctx, hash, raw)
				}
				requireNoError(t, err)
				if first != again || first.SourceRevision != 1 {
					t.Fatalf("retry changed acceptance %+v / %+v", first, again)
				}
				if operation == "save" && first.StateVersion != 2 {
					t.Fatal("retry incremented twice")
				}
				var artifactCount int
				requireNoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM artifacts").Scan(&artifactCount))
				if artifactCount != 1 {
					t.Fatalf("created %d artifacts", artifactCount)
				}
			})
		}
	}
}
