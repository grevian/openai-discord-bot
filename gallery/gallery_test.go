package gallery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// memObject is one object in the fake store.
type memObject struct {
	body []byte
	etag string
	opts PutOptions
}

// memStore is an in-memory ObjectStore with S3's conditional-write semantics.
type memStore struct {
	mu      sync.Mutex
	objects map[string]memObject
	version int
	puts    []string
	// putErr, when set for a key, makes Put fail with that error.
	putErr map[string]error
	// beforePut, when set, runs before every Put's precondition check and may
	// simulate another writer.
	beforePut func(key string)
}

func newMemStore() *memStore {
	return &memStore{objects: map[string]memObject{}, putErr: map[string]error{}}
}

func (s *memStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, "", ErrNotFound
	}
	return append([]byte(nil), o.body...), o.etag, nil
}

func (s *memStore) Put(_ context.Context, key string, body []byte, opts PutOptions) error {
	if s.beforePut != nil {
		s.beforePut(key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.putErr[key]; err != nil {
		return err
	}
	cur, exists := s.objects[key]
	if opts.IfNoneMatch && exists {
		return ErrConflict
	}
	if opts.IfMatch != "" && (!exists || cur.etag != opts.IfMatch) {
		return ErrConflict
	}
	s.version++
	s.objects[key] = memObject{body: append([]byte(nil), body...), etag: fmt.Sprintf("etag-%d", s.version), opts: opts}
	s.puts = append(s.puts, key)
	return nil
}

func (s *memStore) manifest(t *testing.T) Manifest {
	t.Helper()
	body, _, err := s.Get(context.Background(), ManifestKey)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}
	return m
}

func (s *memStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.puts)
}

// clock is a controllable time source that advances one second per call.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func newTestGallery(cfg Config, store ObjectStore) *Gallery {
	g := New(cfg, store)
	c := &clock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	g.now = c.now
	return g
}

func sub(guild string, n int) Submission {
	return Submission{GuildID: guild, ImageKey: fmt.Sprintf("%s/id%03d", guild, n), Prompt: fmt.Sprintf("prompt %d", n), Kind: KindGenerate}
}

var defaultCfg = Config{Enabled: true, MaxItems: 48}

func TestPublishWritesSidecarAndManifest(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)

	if err := g.Publish(context.Background(), sub("g1", 1)); err != nil {
		t.Fatal(err)
	}

	m := store.manifest(t)
	if m.Version != ManifestVersion || len(m.Items) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	e := m.Items[0]
	if e.ID != "id001" || e.Key != "g1/id001" || e.Prompt != "prompt 1" || e.Kind != KindGenerate || e.Style != StyleDanbot {
		t.Errorf("entry = %+v", e)
	}
	if _, _, err := store.Get(context.Background(), ItemPrefix+"id001.json"); err != nil {
		t.Errorf("sidecar missing: %v", err)
	}

	// The manifest must be cacheable for only a minute.
	if cc := store.objects[ManifestKey].opts.CacheControl; cc != "public, max-age=60" {
		t.Errorf("manifest Cache-Control = %q", cc)
	}
	if ct := store.objects[ManifestKey].opts.ContentType; ct != "application/json" {
		t.Errorf("manifest Content-Type = %q", ct)
	}
}

func TestPublishNeverWritesIdentity(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	s := Submission{GuildID: "guild-secret-77", ImageKey: "guild-secret-77/abc", Prompt: "a cat", Kind: KindEdit}
	if err := g.Publish(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// The key legitimately contains the guild ID (it is the image's URL), but
	// the record must carry no field for author, guild or channel.
	for key, o := range store.objects {
		var fields map[string]any
		if json.Unmarshal(o.body, &fields) != nil {
			continue
		}
		for _, banned := range []string{"guild", "guild_id", "author", "user", "user_id", "channel", "username"} {
			if _, ok := fields[banned]; ok {
				t.Errorf("%s has banned field %q", key, banned)
			}
		}
	}
}

func TestPublishOrdersNewestFirst(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	for i := 1; i <= 5; i++ {
		if err := g.Publish(context.Background(), sub("g1", i)); err != nil {
			t.Fatal(err)
		}
	}
	got := ids(store.manifest(t))
	want := []string{"id005", "id004", "id003", "id002", "id001"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestPublishOrdersAcrossGuildsByTime(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	for _, s := range []Submission{sub("g1", 1), sub("g2", 2), sub("g1", 3)} {
		if err := g.Publish(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(ids(store.manifest(t)), ","); got != "id003,id002,id001" {
		t.Errorf("order = %s", got)
	}
}

func TestPublishCapsAtMaxItems(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(Config{Enabled: true, MaxItems: 3}, store)
	for i := 1; i <= 7; i++ {
		if err := g.Publish(context.Background(), sub("g1", i)); err != nil {
			t.Fatal(err)
		}
	}
	got := ids(store.manifest(t))
	if strings.Join(got, ",") != "id007,id006,id005" {
		t.Errorf("kept %v, want the newest three", got)
	}
	// Entries that fell off the manifest are still recorded in their sidecars,
	// for later archives or takedowns.
	if _, _, err := store.Get(context.Background(), ItemPrefix+"id001.json"); err != nil {
		t.Errorf("sidecar of an evicted entry is gone: %v", err)
	}
}

func TestDefaultMaxItems(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(Config{Enabled: true}, store)
	for i := 1; i <= 60; i++ {
		if err := g.Publish(context.Background(), sub("g1", i)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(store.manifest(t).Items); n != 48 {
		t.Errorf("default cap = %d, want 48", n)
	}
}

func TestPublishIsIdempotentPerImage(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	for range 3 {
		if err := g.Publish(context.Background(), sub("g1", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(store.manifest(t).Items); n != 1 {
		t.Errorf("items = %d, want 1", n)
	}
}

func TestDMsAreNeverPublished(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	s := Submission{GuildID: "", ImageKey: "private-chat/abc", Prompt: "secret", Kind: KindGenerate}
	if err := g.Publish(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if n := store.putCount(); n != 0 {
		t.Errorf("a DM image caused %d writes", n)
	}
}

func TestExcludedGuildIsNotPublished(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(Config{Enabled: true, MaxItems: 48, ExcludedGuilds: []string{"bad"}}, store)
	if err := g.Publish(context.Background(), sub("bad", 1)); err != nil {
		t.Fatal(err)
	}
	if n := store.putCount(); n != 0 {
		t.Fatalf("an excluded guild caused %d writes", n)
	}
	// Other guilds are still included by default.
	if err := g.Publish(context.Background(), sub("good", 2)); err != nil {
		t.Fatal(err)
	}
	if got := ids(store.manifest(t)); len(got) != 1 || got[0] != "id002" {
		t.Errorf("items = %v", got)
	}
}

func TestDisabledGalleryPublishesNothing(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(Config{Enabled: false, MaxItems: 48}, store)
	if err := g.Publish(context.Background(), sub("g1", 1)); err != nil {
		t.Fatal(err)
	}
	if n := store.putCount(); n != 0 {
		t.Errorf("disabled gallery wrote %d objects", n)
	}
}

func TestEligible(t *testing.T) {
	c := Config{Enabled: true, ExcludedGuilds: []string{"x"}}
	for guild, want := range map[string]bool{"": false, "x": false, "y": true} {
		if got := c.Eligible(guild); got != want {
			t.Errorf("Eligible(%q) = %v, want %v", guild, got, want)
		}
	}
}

func TestEditKindIsRecorded(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	s := sub("g1", 1)
	s.Kind = KindEdit
	if err := g.Publish(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if k := store.manifest(t).Items[0].Kind; k != KindEdit {
		t.Errorf("kind = %q", k)
	}
	// An unknown kind falls back to generate.
	s = sub("g1", 2)
	s.Kind = "bogus"
	if err := g.Publish(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if k := store.manifest(t).Items[0].Kind; k != KindGenerate {
		t.Errorf("kind = %q", k)
	}
}

// Concurrent submissions inside one process are serialised by the mutex: none
// is lost and no conditional write ever fails.
func TestConcurrentPublishesLoseNothing(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(Config{Enabled: true, MaxItems: 100}, store)

	const n = 40
	var wg sync.WaitGroup
	var failed atomic.Int32
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Publish(context.Background(), sub("g1", i)); err != nil {
				failed.Add(1)
				t.Errorf("publish %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if failed.Load() != 0 {
		t.FailNow()
	}
	if got := len(store.manifest(t).Items); got != n {
		t.Errorf("manifest has %d items, want %d", got, n)
	}
}

// Two Gallery values sharing a bucket stand in for two bot instances (a rolling
// deploy). The mutex does not help across them; the conditional write does.
func TestConcurrentInstancesRetryOnConflict(t *testing.T) {
	store := newMemStore()
	cfg := Config{Enabled: true, MaxItems: 100}
	a, b := newTestGallery(cfg, store), newTestGallery(cfg, store)

	const n = 30
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		g := a
		if i%2 == 0 {
			g = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Publish(context.Background(), sub("g1", i)); err != nil {
				t.Errorf("publish %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if got := len(store.manifest(t).Items); got != n {
		t.Errorf("manifest has %d items, want %d (an update was lost)", got, n)
	}
}

// A single deterministic interleaving: another writer lands between our read
// and our write. The conditional write must refuse ours, and the retry must
// re-read and keep both entries.
func TestLostRaceIsDetectedAndRetried(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	other := newTestGallery(defaultCfg, store)
	if err := g.Publish(context.Background(), sub("g1", 1)); err != nil {
		t.Fatal(err)
	}

	var injected bool
	store.beforePut = func(key string) {
		if key != ManifestKey || injected {
			return
		}
		injected = true
		store.beforePut = nil // the interloper's own writes must not recurse
		if err := other.addToManifest(context.Background(), Entry{ID: "id002", Key: "g2/id002", Created: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), Prompt: "interloper"}); err != nil {
			t.Errorf("interloper: %v", err)
		}
	}
	if err := g.Publish(context.Background(), sub("g1", 3)); err != nil {
		t.Fatal(err)
	}
	if !injected {
		t.Fatal("race was never injected")
	}
	got := ids(store.manifest(t))
	if len(got) != 3 {
		t.Errorf("items = %v, want all three entries", got)
	}
}

func TestFirstWriteRacesAreDetected(t *testing.T) {
	// Both writers see no manifest. Only one create can win (If-None-Match).
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	other := newTestGallery(defaultCfg, store)

	var injected bool
	store.beforePut = func(key string) {
		if key != ManifestKey || injected {
			return
		}
		injected = true
		store.beforePut = nil
		if err := other.addToManifest(context.Background(), Entry{ID: "id009", Key: "g2/id009", Created: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}); err != nil {
			t.Errorf("interloper: %v", err)
		}
	}
	if err := g.Publish(context.Background(), sub("g1", 1)); err != nil {
		t.Fatal(err)
	}
	if got := len(store.manifest(t).Items); got != 2 {
		t.Errorf("items = %d, want 2", got)
	}
}

func TestGivesUpAfterRepeatedConflicts(t *testing.T) {
	store := newMemStore()
	store.putErr[ManifestKey] = ErrConflict
	g := newTestGallery(defaultCfg, store)
	err := g.Publish(context.Background(), sub("g1", 1))
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestRetryStopsWhenContextEnds(t *testing.T) {
	store := newMemStore()
	store.putErr[ManifestKey] = ErrConflict
	g := newTestGallery(defaultCfg, store)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := g.Publish(ctx, sub("g1", 1))
	if err == nil {
		t.Fatal("want an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v: retries ignored the deadline", time.Since(start))
	}
}

func TestSidecarFailureLeavesManifestUntouched(t *testing.T) {
	store := newMemStore()
	store.putErr[ItemPrefix+"id001.json"] = errors.New("boom")
	g := newTestGallery(defaultCfg, store)
	if err := g.Publish(context.Background(), sub("g1", 1)); err == nil {
		t.Fatal("want an error")
	}
	if _, _, err := store.Get(context.Background(), ManifestKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("manifest was written despite the sidecar failing: %v", err)
	}
}

func TestCorruptManifestIsNotOverwritten(t *testing.T) {
	store := newMemStore()
	store.objects[ManifestKey] = memObject{body: []byte("{not json"), etag: "e"}
	g := newTestGallery(defaultCfg, store)
	if err := g.Publish(context.Background(), sub("g1", 1)); err == nil {
		t.Fatal("want an error")
	}
	if string(store.objects[ManifestKey].body) != "{not json" {
		t.Error("a corrupt manifest was replaced, discarding the gallery")
	}
}

func TestMissingImageKeyIsRejected(t *testing.T) {
	g := newTestGallery(defaultCfg, newMemStore())
	if err := g.Publish(context.Background(), Submission{GuildID: "g1"}); err == nil {
		t.Error("want an error")
	}
}

func TestCleanPrompt(t *testing.T) {
	long := strings.Repeat("a", 400)
	tests := []struct{ name, in, want string }{
		{"plain", "a cat on a train", "a cat on a train"},
		{"whitespace collapsed", "  a \n cat\t\ton   a train ", "a cat on a train"},
		{"user mention", "draw <@123456789> and <@!987654321>", "draw someone and someone"},
		{"role and channel mentions", "<@&5555> in <#6666>", "someone in someone"},
		{"broadcast mentions", "hey @everyone and @here and @Everyone", "hey everyone and here and Everyone"},
		{"only the broadcast words", "me@here.example and @hereford", "me@here.example and @hereford"},
		{"empty", "   ", ""},
		{"html is left alone for the renderer", "<script>alert(1)</script>", "<script>alert(1)</script>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanPrompt(tt.in); got != tt.want {
				t.Errorf("CleanPrompt(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	got := CleanPrompt(long)
	if r := []rune(got); len(r) != maxPromptRunes || r[len(r)-1] != '…' {
		t.Errorf("long prompt: %d runes, ends %q", len(r), string(r[len(r)-1]))
	}
	// Truncation must not split a multi-byte rune.
	if got := CleanPrompt(strings.Repeat("🚂", 400)); !strings.HasSuffix(got, "🚂…") {
		t.Errorf("multi-byte prompt truncated badly: %q", got[len(got)-12:])
	}
}

// The manifest is JSON, so markup in a prompt reaches the page as inert
// < escapes, never as raw angle brackets.
func TestManifestEscapesMarkup(t *testing.T) {
	store := newMemStore()
	g := newTestGallery(defaultCfg, store)
	s := sub("g1", 1)
	s.Prompt = `<img src=x onerror=alert(1)> & "quotes"</script>`
	if err := g.Publish(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{ManifestKey, ItemPrefix + "id001.json"} {
		body := string(store.objects[key].body)
		if strings.ContainsAny(body, "<>") || strings.Contains(body, "&") {
			t.Errorf("%s contains raw markup characters: %s", key, body)
		}
		var back any
		if err := json.Unmarshal([]byte(body), &back); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
	// And it round-trips to the original text.
	if got := store.manifest(t).Items[0].Prompt; got != CleanPrompt(s.Prompt) {
		t.Errorf("round trip = %q", got)
	}
}

func ids(m Manifest) []string {
	out := make([]string, len(m.Items))
	for i, e := range m.Items {
		out[i] = e.ID
	}
	return out
}
