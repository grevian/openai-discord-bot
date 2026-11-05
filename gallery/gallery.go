// Package gallery publishes danbot's generated images to a public, static
// gallery page.
//
// The gallery is a fixed page (index.html + gallery.js + gallery.css) that
// fetches a JSON manifest, latest.json, listing the newest images. Everything
// lives under the "gallery/" prefix of the existing images bucket, which the
// existing CloudFront distribution already serves, so it needs no new
// infrastructure.
//
// For every published image the Gallery writes two small objects:
//
//   - gallery/items/<id>.json: a sidecar holding the full record. It is the
//     durable source of truth, which later phases can use to take an image
//     down or to rebuild the manifest and archives.
//   - gallery/latest.json: the newest MaxItems entries, newest first.
//
// The author, guild and channel are never written to either object.
package gallery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Object keys, relative to the bucket root.
const (
	// Prefix is the key prefix everything the gallery owns lives under.
	Prefix = "gallery/"
	// ManifestKey is the manifest the page loads.
	ManifestKey = Prefix + "latest.json"
	// ItemPrefix is where the per-image sidecars live.
	ItemPrefix = Prefix + "items/"
)

// Kind says how an image was made.
type Kind string

const (
	KindGenerate Kind = "generate"
	KindEdit     Kind = "edit"
)

// Style is the persona an image was drawn in. Only the danbot style exists
// today; recording it lets a future "clean" style be filtered out.
const StyleDanbot = "danbot"

const (
	// ManifestVersion is the schema version of latest.json.
	ManifestVersion = 1
	// maxPromptRunes caps the prompt shown on the page.
	maxPromptRunes = 280
	// manifestCacheControl lets CloudFront and browsers cache the manifest
	// for a minute. Without it CloudFront's default is 24 hours.
	manifestCacheControl = "public, max-age=60"
	// maxUpdateAttempts bounds the optimistic-concurrency retries.
	maxUpdateAttempts = 6
)

// Entry is one image in the gallery. It is the public record: nothing here
// identifies who asked for the image or where.
type Entry struct {
	// ID is the image's ksuid, unique and time-sortable.
	ID string `json:"id"`
	// Key is the image's key in the images bucket, served at /<key>.
	Key string `json:"key"`
	// Created is when the image was generated.
	Created time.Time `json:"created"`
	// Prompt is the user's prompt, cleaned and truncated.
	Prompt string `json:"prompt"`
	Kind   Kind   `json:"kind"`
	Style  string `json:"style"`
}

// Manifest is the content of latest.json.
type Manifest struct {
	Version int       `json:"version"`
	Updated time.Time `json:"updated"`
	// Items are newest first.
	Items []Entry `json:"items"`
}

// Submission is what the bot knows about a freshly generated image. Unlike an
// Entry it carries the guild, which only decides eligibility and is never
// published.
type Submission struct {
	// GuildID is empty for DMs.
	GuildID string
	// ImageKey is the key StoreImage returned.
	ImageKey string
	Prompt   string
	Kind     Kind
}

// Config is the gallery's policy. It is checked in as config.json so that
// changing it needs no terraform.
type Config struct {
	// Enabled is the master switch.
	Enabled bool `json:"enabled"`
	// MaxItems is how many entries latest.json holds.
	MaxItems int `json:"max_items"`
	// ExcludedGuilds are guild IDs whose images are never published. Guilds
	// are included by default; this is the opt-out.
	ExcludedGuilds []string `json:"excluded_guilds"`
}

func (c Config) excluded(guildID string) bool {
	for _, g := range c.ExcludedGuilds {
		if g == guildID {
			return true
		}
	}
	return false
}

// Eligible reports whether images from guildID may be published. DMs (an
// empty guild ID) are never eligible.
func (c Config) Eligible(guildID string) bool {
	return c.Enabled && guildID != "" && !c.excluded(guildID)
}

// ErrNotFound is returned by ObjectStore.Get for a missing object.
var ErrNotFound = errors.New("gallery: object not found")

// ErrConflict is returned by ObjectStore.Put when a conditional write loses a
// race: the object changed (or appeared) since it was read.
var ErrConflict = errors.New("gallery: object changed concurrently")

// PutOptions are the headers and preconditions of one write.
type PutOptions struct {
	ContentType  string
	CacheControl string
	// IfMatch makes the write succeed only if the object's ETag still equals
	// this value.
	IfMatch string
	// IfNoneMatch makes the write succeed only if the object does not exist.
	IfNoneMatch bool
}

// ObjectStore is the gallery's view of the bucket. *S3Store satisfies it;
// tests use an in-memory fake.
type ObjectStore interface {
	// Get returns the object's body and ETag, or ErrNotFound.
	Get(ctx context.Context, key string) (body []byte, etag string, err error)
	// Put writes an object. A failed precondition returns ErrConflict.
	Put(ctx context.Context, key string, body []byte, opts PutOptions) error
}

// Gallery publishes eligible images. It is safe for concurrent use.
type Gallery struct {
	cfg   Config
	store ObjectStore
	now   func() time.Time

	// mu serialises manifest updates inside this process, so concurrent
	// submissions normally never see a conflict at all.
	mu sync.Mutex
}

// New returns a Gallery. A non-positive MaxItems means the default of 48.
func New(cfg Config, store ObjectStore) *Gallery {
	if cfg.MaxItems <= 0 {
		cfg.MaxItems = 48
	}
	return &Gallery{cfg: cfg, store: store, now: time.Now}
}

// Publish adds an image to the gallery if its guild is eligible. It returns
// nil without writing anything for DMs, excluded guilds, or a disabled
// gallery. The caller decides how to treat errors; the bot only logs them.
func (g *Gallery) Publish(ctx context.Context, s Submission) error {
	if !g.cfg.Eligible(s.GuildID) {
		return nil
	}
	entry, err := g.entryFor(s)
	if err != nil {
		return err
	}

	// Sidecar first: it is the durable record. If it fails there is nothing
	// half-published, because the manifest has not been touched.
	sidecar, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal sidecar: %w", err)
	}
	if err := g.store.Put(ctx, ItemPrefix+entry.ID+".json", sidecar, PutOptions{ContentType: "application/json"}); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}
	return g.addToManifest(ctx, entry)
}

// entryFor builds the public record for a submission.
func (g *Gallery) entryFor(s Submission) (Entry, error) {
	id := s.ImageKey
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	if id == "" || s.ImageKey == "" {
		return Entry{}, errors.New("submission has no image key")
	}
	kind := s.Kind
	if kind != KindEdit {
		kind = KindGenerate
	}
	return Entry{
		ID:      id,
		Key:     s.ImageKey,
		Created: g.now().UTC().Truncate(time.Second),
		Prompt:  CleanPrompt(s.Prompt),
		Kind:    kind,
		Style:   StyleDanbot,
	}, nil
}

// addToManifest inserts entry into latest.json.
//
// latest.json is a read-modify-write on a single object, so two writers can
// lose each other's update. Two layers prevent that:
//
//  1. A mutex serialises writers inside this process. The bot runs as a single
//     instance, so this alone makes conflicts impossible in practice.
//  2. Every write is conditional on the ETag that was read (If-Match), or on
//     the object not existing yet (If-None-Match: *). If another writer got in
//     between (a second instance during a rolling deploy, say), S3 refuses the
//     write, and we re-read, re-apply the entry to the fresh manifest and try
//     again. Applying is idempotent (entries are merged by ID), so a retry can
//     never duplicate an entry.
func (g *Gallery) addToManifest(ctx context.Context, entry Entry) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		if attempt > 0 {
			// Back off a little, with the attempt number as jitter between
			// contending writers, but never past the caller's deadline.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
			}
		}

		current, etag, err := g.loadManifest(ctx)
		if err != nil {
			return err
		}
		next := g.apply(current, entry)
		body, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("marshal manifest: %w", err)
		}

		opts := PutOptions{ContentType: "application/json", CacheControl: manifestCacheControl}
		if etag == "" {
			opts.IfNoneMatch = true
		} else {
			opts.IfMatch = etag
		}
		err = g.store.Put(ctx, ManifestKey, body, opts)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrConflict) {
			return fmt.Errorf("write manifest: %w", err)
		}
		lastErr = err
		slog.Default().WarnContext(ctx, "gallery manifest changed underneath us, retrying",
			slog.Int("attempt", attempt+1))
	}
	return fmt.Errorf("write manifest: gave up after %d attempts: %w", maxUpdateAttempts, lastErr)
}

// loadManifest reads latest.json. A missing manifest is an empty one with an
// empty ETag. A manifest that cannot be parsed is an error rather than an
// empty one: overwriting it would silently throw the gallery away.
func (g *Gallery) loadManifest(ctx context.Context) (Manifest, string, error) {
	body, etag, err := g.store.Get(ctx, ManifestKey)
	if errors.Is(err, ErrNotFound) {
		return Manifest{Version: ManifestVersion}, "", nil
	}
	if err != nil {
		return Manifest{}, "", fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, "", fmt.Errorf("parse manifest: %w", err)
	}
	return m, etag, nil
}

// apply returns m with entry merged in: de-duplicated by ID, sorted newest
// first (ID, a ksuid, breaks ties), and capped at MaxItems.
func (g *Gallery) apply(m Manifest, entry Entry) Manifest {
	items := make([]Entry, 0, len(m.Items)+1)
	items = append(items, entry)
	for _, e := range m.Items {
		if e.ID != entry.ID {
			items = append(items, e)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Created.Equal(items[j].Created) {
			return items[i].Created.After(items[j].Created)
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > g.cfg.MaxItems {
		items = items[:g.cfg.MaxItems]
	}
	return Manifest{Version: ManifestVersion, Updated: g.now().UTC().Truncate(time.Second), Items: items}
}

// mentionRE matches Discord user, role and channel mentions. They contain
// numeric IDs, which must not be published.
var mentionRE = regexp.MustCompile(`<(?:@[!&]?|#)\d+>`)

// CleanPrompt prepares a user's prompt for publication: Discord mentions are
// replaced (they carry user IDs), whitespace is collapsed, and the result is
// truncated. It does not HTML-escape anything: the page renders prompts with
// textContent and the manifest is JSON, which escapes <, > and &.
func CleanPrompt(p string) string {
	p = mentionRE.ReplaceAllString(p, "someone")
	p = strings.Join(strings.Fields(p), " ")
	if utf8.RuneCountInString(p) > maxPromptRunes {
		r := []rune(p)
		p = strings.TrimSpace(string(r[:maxPromptRunes-1])) + "…"
	}
	return p
}
