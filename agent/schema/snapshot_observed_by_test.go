package schema

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func richObservedByMeta(id string) SessionMeta {
	loopDetection := true
	return SessionMeta{
		ID:         id,
		ProfileID:  "profile-rich",
		Model:      "provider/model-rich",
		CheapModel: "provider/model-cheap",
		Config: ConfigSnapshot{
			MaxToolRoundsPerInput:  17,
			ToolOutputLimits:       map[string]ToolOutputLimit{"shell": {MaxChars: 4321}},
			SkillsDirs:             []string{"/skills/a", "/skills/b"},
			EnableLoopDetection:    &loopDetection,
			ShareTasksWithChildren: true,
		},
		EnvInfo: EnvironmentInfo{
			WorkingDir:            "/worktree",
			Platform:              "test-os",
			Today:                 "2026-08-01",
			IsGitRepo:             true,
			GitBranch:             "wip/test",
			GitRecentCommitTitles: []string{"one", "two"},
		},
		CreatedAt:          time.Unix(100, 0).UTC(),
		UpdatedAt:          time.Unix(200, 0).UTC(),
		TurnCount:          7,
		AcceptedInputTurns: 6,
		LastInputTokens:    1234,
		Name:               "rich worker",
		NameSource:         "prompt",
		OriginalPrompt:     "preserve every ordinary field",
		IsSubagent:         true,
		Origin:             "test",
		Goal: &GoalSnapshot{
			Objective: "finish",
			Status:    "active",
		},
		PinnedNote:      "keep this",
		ObservedBy:      []string{"observer_existing"},
		WorktreePath:    "/worktree",
		WorktreeManaged: true,
		CumulativeUsage: CumulativeUsage{
			InputTokens:  101,
			OutputTokens: 202,
			TotalTokens:  303,
		},
		WorkMillis: 404,
	}
}

func TestSaveSessionMetaPreservesObservedBy(t *testing.T) {
	dir := t.TempDir()
	const worker = "02wMz5TxvEMoJEDTDGOTil"
	const observer = "02wMz5TxvCu3kdckfnw0Gh"
	if err := SaveSessionMeta(dir, SessionMeta{ID: worker, ObservedBy: []string{observer}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionMeta(dir, SessionMeta{ID: worker, Name: "new", TurnCount: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionMeta(dir, worker)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "new" || got.TurnCount != 2 || len(got.ObservedBy) != 1 || got.ObservedBy[0] != observer {
		t.Fatalf("meta = %+v", got)
	}
}

func TestAppendSessionObservedByPreservesFieldsAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	const worker = "02wMz5TxvEMoJEDTDGOTil"
	seeded := richObservedByMeta(worker)
	if err := SaveSessionMeta(dir, seeded); err != nil {
		t.Fatal(err)
	}
	for _, observer := range []string{"observer_a", "observer_b", "observer_a", "observer_existing"} {
		if err := AppendSessionObservedBy(dir, worker, observer); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadSessionMeta(dir, worker)
	if err != nil {
		t.Fatal(err)
	}
	want := seeded
	want.ObservedBy = []string{"observer_existing", "observer_a", "observer_b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("meta after observer appends\n got: %#v\nwant: %#v", got, want)
	}
}

type blockingRenameFS struct {
	afero.Fs
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRenameFS(fs afero.Fs) *blockingRenameFS {
	return &blockingRenameFS{
		Fs:      fs,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (fs *blockingRenameFS) Rename(oldname, newname string) error {
	fs.entered <- struct{}{}
	<-fs.release
	return fs.Fs.Rename(oldname, newname)
}

func (fs *blockingRenameFS) releaseRename() {
	fs.once.Do(func() { close(fs.release) })
}

func TestSessionMetaWritesSerializeObserverAppend(t *testing.T) {
	mem := afero.NewMemMapFs()
	const (
		dir      = "/state"
		worker   = "02wMz5TxvEMoJEDTDGOTil"
		observer = "observer_new"
	)
	seeded := richObservedByMeta(worker)
	if err := SaveSessionMetaWithFS(mem, dir, seeded); err != nil {
		t.Fatal(err)
	}

	saveFS := newBlockingRenameFS(mem)
	appendFS := newBlockingRenameFS(mem)
	t.Cleanup(saveFS.releaseRename)
	t.Cleanup(appendFS.releaseRename)

	saved := seeded
	saved.Name = "saved while observer waits"
	saved.TurnCount = 9
	saved.LastInputTokens = 9876
	saved.ObservedBy = nil
	saveDone := make(chan error, 1)
	go func() { saveDone <- SaveSessionMetaWithFS(saveFS, dir, saved) }()
	<-saveFS.entered
	if lock := sessionMetaWriteLock(worker); lock.TryLock() {
		lock.Unlock()
		t.Fatal("whole-meta save reached rename without holding the session's meta write lock")
	}

	appendDone := make(chan error, 1)
	go func() { appendDone <- appendSessionObservedByWithFS(appendFS, dir, worker, observer) }()
	saveFS.releaseRename()
	if err := <-saveDone; err != nil {
		t.Fatalf("whole-meta save: %v", err)
	}

	<-appendFS.entered
	if lock := sessionMetaWriteLock(worker); lock.TryLock() {
		lock.Unlock()
		t.Fatal("observer append reached rename without holding the session's meta write lock")
	}
	appendFS.releaseRename()
	if err := <-appendDone; err != nil {
		t.Fatalf("observer append: %v", err)
	}

	got, err := LoadSessionMetaWithFS(mem, dir, worker)
	if err != nil {
		t.Fatal(err)
	}
	want := saved
	want.ObservedBy = []string{"observer_existing", observer}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serialized meta\n got: %#v\nwant: %#v", got, want)
	}
}
