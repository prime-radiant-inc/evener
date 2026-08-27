package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNavigationCacheConcurrentMissBuildsOnce(t *testing.T) {
	cache := newNavigationRepresentationCache(256, 64<<20)
	key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1", Generation: "generation-a", Revision: 4}
	var calls atomic.Int32
	buildStarted := make(chan struct{})
	buildRelease := make(chan struct{})
	var started atomic.Bool
	build := func(context.Context) (navigationRepresentation, error) {
		calls.Add(1)
		if started.CompareAndSwap(false, true) {
			close(buildStarted)
			<-buildRelease
		}
		return representationFixture("project:p1", 4), nil
	}

	const callers = 20
	results := make(chan navigationRepresentation, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			representation, err := cache.Get(context.Background(), key, build)
			results <- representation
			errs <- err
		})
	}
	<-buildStarted
	close(buildRelease)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first navigationRepresentation
	for representation := range results {
		if first.JSON == nil {
			first = representation
			continue
		}
		if !bytes.Equal(representation.JSON, first.JSON) || representation.ETag != first.ETag {
			t.Fatal("coalesced callers received different representations")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("build calls=%d, want 1", got)
	}
	stats := cache.Stats()
	if stats.Misses != 1 || stats.Coalesced == 0 {
		t.Fatalf("stats=%+v, want one miss and coalesced waiters", stats)
	}
}

func TestNavigationCacheAtomicOwnerRegistration(t *testing.T) {
	cache := newNavigationRepresentationCache(256, 64<<20)
	key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "ordered", Generation: "generation-a", Revision: 1}
	var calls atomic.Int32
	ownerHookEntered := make(chan struct{})
	allowRegistration := make(chan struct{})
	buildRelease := make(chan struct{})
	var ownerHook atomic.Bool
	var waiterHooks atomic.Int32
	cache.beforeSingleflight = func(owner bool) {
		if owner && ownerHook.CompareAndSwap(false, true) {
			close(ownerHookEntered)
			<-allowRegistration
			return
		}
		if !owner {
			if waiterHooks.Add(1) == 19 {
				close(buildRelease)
			}
		}
	}
	build := func(context.Context) (navigationRepresentation, error) {
		calls.Add(1)
		<-buildRelease
		return representationFixture("ordered", 1), nil
	}

	const callers = 20
	results := make(chan error, callers)
	go func() {
		_, err := cache.Get(context.Background(), key, build)
		results <- err
	}()
	<-ownerHookEntered
	for range callers - 1 {
		go func() {
			_, err := cache.Get(context.Background(), key, build)
			results <- err
		}()
	}
	close(allowRegistration)
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("build calls=%d, want 1", got)
	}
	if got := waiterHooks.Load(); got != callers-1 {
		t.Fatalf("waiter registrations=%d, want %d", got, callers-1)
	}
	fixture := representationFixture("ordered", 1)
	stats := cache.Stats()
	if stats.Misses != 1 || stats.Coalesced != callers-1 || stats.Hits != 0 || stats.Entries != 1 || stats.Bytes != fixture.SizeEstimate {
		t.Fatalf("stats=%+v, want one miss, %d waiters, no hits, one entry", stats, callers-1)
	}
}

func TestNavigationCacheKeyCanonicalIdentity(t *testing.T) {
	base := navigationResourceKey{Kind: navigationResourceLive, Offset: 3, Generation: "generation-a", Revision: 7}
	if got, want := base.canonical().Limit, uint32(maxNavigationSectionRows); got != want {
		t.Fatalf("default limit=%d, want %d", got, want)
	}
	if base.String() != (navigationResourceKey{Kind: navigationResourceLive, Offset: 3, Limit: maxNavigationSectionRows, Generation: "generation-a", Revision: 7}).String() {
		t.Fatal("default section limit created a duplicate identity")
	}
	if base.String() == (navigationResourceKey{Kind: navigationResourceLive, Offset: 4, Limit: maxNavigationSectionRows, Generation: "generation-a", Revision: 7}).String() {
		t.Fatal("offset was omitted from identity")
	}
	if base.String() == (navigationResourceKey{Kind: navigationResourceLive, Offset: 3, Limit: 25, Generation: "generation-a", Revision: 7}).String() {
		t.Fatal("limit was omitted from identity")
	}
	if base.String() == (navigationResourceKey{Kind: navigationResourceNeedsYou, Offset: 3, Limit: maxNavigationSectionRows, Generation: "generation-a", Revision: 7}).String() {
		t.Fatal("kind was omitted from identity")
	}
	if base.String() == (navigationResourceKey{Kind: navigationResourceLive, Offset: 3, Limit: maxNavigationSectionRows, Generation: "generation-b", Revision: 7}).String() {
		t.Fatal("generation was omitted from identity")
	}
	if base.String() == (navigationResourceKey{Kind: navigationResourceLive, Offset: 3, Limit: maxNavigationSectionRows, Generation: "generation-a", Revision: 8}).String() {
		t.Fatal("revision was omitted from identity")
	}

	pinByID := navigationResourceKey{Kind: navigationResourcePinSection, ID: "section:with|delimiter", Offset: 1, Limit: 0}
	pinBySection := navigationResourceKey{Kind: navigationResourcePinSection, SectionID: "section:with|delimiter", Offset: 1, Limit: maxNavigationSectionRows}
	if pinByID.String() != pinBySection.String() {
		t.Fatal("pin section aliases did not canonicalize")
	}
	if (navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "a|b"}).String() == (navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "a", ID: "b"}).String() {
		t.Fatal("field boundaries permit a project-key collision")
	}
}

func TestNavigationCacheBuildsBothEncodingsOnce(t *testing.T) {
	cache := newNavigationRepresentationCache(256, 64<<20)
	key := navigationResourceKey{Kind: navigationResourceLocation, ID: "session-1", Generation: "generation-a", Revision: 2}
	var calls atomic.Int32
	build := func(context.Context) (navigationRepresentation, error) {
		calls.Add(1)
		return representationFixture("location", 2), nil
	}
	first, err := cache.Get(context.Background(), key, build)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.Get(context.Background(), key, build)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !bytes.Equal(first.JSON, second.JSON) || !bytes.Equal(first.Gzip, second.Gzip) {
		t.Fatal("warm lookup rebuilt or changed encoded representation")
	}
	reader, err := gzip.NewReader(bytes.NewReader(first.Gzip))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decompressed, first.JSON) {
		t.Fatal("gzip representation does not decode to JSON")
	}
	if first.ETag != navigationETag(key, first.Generation, first.Revision) {
		t.Fatalf("ETag=%q, want exact weak tag", first.ETag)
	}
}

func TestNavigationCacheETagExpectedValue(t *testing.T) {
	key := navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "recent", Offset: 17, Limit: 50, Generation: "generation-a", Revision: 9}
	got := navigationETag(key, "generation-a", 9)
	sum := sha256.Sum256([]byte(key.String()))
	want := fmt.Sprintf(`W/"nav-generation-a-%x-9"`, sum)
	if got != want {
		t.Fatalf("ETag=%q, want %q", got, want)
	}
	if got == navigationETag(key, "generation-b", 9) || got == navigationETag(key, "generation-a", 10) {
		t.Fatal("ETag did not vary with generation or revision")
	}
}

func TestNavigationCacheLRUEntryAndByteBounds(t *testing.T) {
	cache := newNavigationRepresentationCache(2, 100)
	build := func(identity string, size int64) func(context.Context) (navigationRepresentation, error) {
		return func(context.Context) (navigationRepresentation, error) {
			representation := representationFixture(identity, 1)
			representation.SizeEstimate = size
			return representation, nil
		}
	}
	keyA := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "a", Generation: "generation-a", Revision: 1}
	keyB := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "b", Generation: "generation-a", Revision: 1}
	keyC := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "c", Generation: "generation-a", Revision: 1}
	if _, err := cache.Get(context.Background(), keyA, build("a", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(context.Background(), keyB, build("b", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(context.Background(), keyA, build("a-rebuild", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(context.Background(), keyC, build("c", 40)); err != nil {
		t.Fatal(err)
	}
	if got := cache.Stats(); got.Entries != 2 || got.Bytes != 80 || got.Evictions != 1 {
		t.Fatalf("entry LRU stats=%+v, want two entries, 80 bytes, one eviction", got)
	}
	if _, err := cache.Get(context.Background(), keyB, build("b-rebuild", 40)); err != nil {
		t.Fatal(err)
	}
	if got := cache.Stats(); got.Evictions != 2 {
		t.Fatalf("B was not the LRU entry: stats=%+v", got)
	}

	byteCache := newNavigationRepresentationCache(10, 64)
	for _, item := range []struct {
		key  navigationResourceKey
		name string
	}{
		{navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "x", Generation: "generation-a", Revision: 1}, "x"},
		{navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "y", Generation: "generation-a", Revision: 1}, "y"},
	} {
		if _, err := byteCache.Get(context.Background(), item.key, build(item.name, 40)); err != nil {
			t.Fatal(err)
		}
	}
	if got := byteCache.Stats(); got.Entries != 1 || got.Bytes != 40 || got.Evictions != 1 {
		t.Fatalf("byte-bound stats=%+v, want one retained entry and one eviction", got)
	}
}

func TestNavigationCacheBuildFailureAndCancellation(t *testing.T) {
	cache := newNavigationRepresentationCache(256, 64<<20)
	key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "failure", Generation: "generation-a", Revision: 1}
	var calls atomic.Int32
	build := func(context.Context) (navigationRepresentation, error) {
		if calls.Add(1) == 1 {
			return navigationRepresentation{}, errors.New("build failed")
		}
		return representationFixture("retry", 1), nil
	}
	if _, err := cache.Get(context.Background(), key, build); err == nil {
		t.Fatal("failed build returned nil error")
	}
	if _, err := cache.Get(context.Background(), key, build); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || cache.Stats().Entries != 1 {
		t.Fatalf("failure/retry state calls=%d stats=%+v", calls.Load(), cache.Stats())
	}

	ownerKey := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "cancel", Generation: "generation-a", Revision: 1}
	ownerRelease := make(chan struct{})
	ownerStarted := make(chan struct{})
	owner := func(context.Context) (navigationRepresentation, error) {
		close(ownerStarted)
		<-ownerRelease
		return representationFixture("owner", 1), nil
	}
	ownerDone := make(chan error, 1)
	go func() {
		_, err := cache.Get(context.Background(), ownerKey, owner)
		ownerDone <- err
	}()
	<-ownerStarted
	waiterContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.Get(waiterContext, ownerKey, owner); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error=%v, want context canceled", err)
	}
	close(ownerRelease)
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
}

func TestNavigationCacheRejectsVersionMismatchesAndRetries(t *testing.T) {
	cache := newNavigationRepresentationCache(256, 64<<20)
	key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "versioned", Generation: "generation-a", Revision: 7}
	var calls atomic.Int32
	build := func(context.Context) (navigationRepresentation, error) {
		if calls.Add(1) == 1 {
			wrong := representationFixture("wrong-generation", 7)
			wrong.Generation = "generation-b"
			return wrong, nil
		}
		return representationFixture("correct", 7), nil
	}
	if _, err := cache.Get(context.Background(), key, build); err == nil {
		t.Fatal("generation mismatch returned nil error")
	}
	if _, err := cache.Get(context.Background(), key, build); err != nil {
		t.Fatal(err)
	}
	if got := cache.Stats(); got.Entries != 1 || got.Misses != 2 {
		t.Fatalf("generation retry stats=%+v", got)
	}

	revisionKey := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "revisioned", Generation: "generation-a", Revision: 8}
	if _, err := cache.Get(context.Background(), revisionKey, func(context.Context) (navigationRepresentation, error) {
		return representationFixture("wrong-revision", 9), nil
	}); err == nil {
		t.Fatal("revision mismatch returned nil error")
	}
	if _, err := cache.Get(context.Background(), revisionKey, func(context.Context) (navigationRepresentation, error) {
		return representationFixture("correct-revision", 8), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := cache.Stats(); got.Entries != 2 {
		t.Fatalf("mismatched revision was published: stats=%+v", got)
	}
}

func TestNavigationCacheRejectsUnsafeGenerations(t *testing.T) {
	unsafe := []string{"generation with space", "generation\"quote", "generation\\slash", "generation\r\nheader"}
	for _, generation := range unsafe {
		cache := newNavigationRepresentationCache(256, 64<<20)
		calls := atomic.Int32{}
		key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "unsafe", Generation: generation, Revision: 1}
		if _, err := cache.Get(context.Background(), key, func(context.Context) (navigationRepresentation, error) {
			calls.Add(1)
			return representationFixture("unsafe", 1), nil
		}); err == nil {
			t.Fatalf("generation %q was accepted", generation)
		}
		if calls.Load() != 0 || cache.Stats().Misses != 0 {
			t.Fatalf("unsafe key generation reached builder: generation=%q stats=%+v", generation, cache.Stats())
		}
	}

	cache := newNavigationRepresentationCache(256, 64<<20)
	key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "unsafe-result", Generation: "generation-a", Revision: 1}
	if _, err := cache.Get(context.Background(), key, func(context.Context) (navigationRepresentation, error) {
		return navigationRepresentation{Generation: "generation\r\n", Revision: 1, SizeEstimate: 1}, nil
	}); err == nil {
		t.Fatal("unsafe result generation was accepted")
	}
	if got := cache.Stats(); got.Entries != 0 {
		t.Fatalf("unsafe result generation was published: stats=%+v", got)
	}
}

func representationFixture(identity string, revision uint64) navigationRepresentation {
	object := struct {
		Identity string `json:"identity"`
		Revision uint64 `json:"revision"`
	}{Identity: identity, Revision: revision}
	jsonBytes, err := json.Marshal(object)
	if err != nil {
		panic(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(jsonBytes); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return navigationRepresentation{
		Object:       object,
		JSON:         jsonBytes,
		Gzip:         compressed.Bytes(),
		Generation:   "generation-a",
		Revision:     revision,
		SizeEstimate: int64(len(jsonBytes) + compressed.Len()),
	}
}
