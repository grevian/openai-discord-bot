package flags

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

var (
	// ErrUnknownFlag is returned by Set for a name that is not registered.
	ErrUnknownFlag = errors.New("unknown flag")
	// ErrInvalidValue is returned by Set for a value the flag's kind can't hold.
	ErrInvalidValue = errors.New("invalid flag value")
	// ErrNoBackend is returned by Set when no backend is configured.
	ErrNoBackend = errors.New("no flag storage configured")
)

// Value is a flag's stored value and who last changed it.
type Value struct {
	Name      string
	Raw       string
	UpdatedBy string
	UpdatedAt time.Time
	PrevValue string
}

// Change is one flag change, as written to the backend and its audit trail.
// Old and New are canonical values.
type Change struct {
	Name string
	Old  string
	New  string
	By   string
	At   time.Time
}

// Unchanged reports whether the change set a flag to the value it had.
func (c Change) Unchanged() bool { return c.Old == c.New }

// Backend stores flag values.
type Backend interface {
	// LoadAll returns the stored value of every flag, keyed by name. It may
	// include names that are no longer registered.
	LoadAll(ctx context.Context) (map[string]Value, error)
	// Put stores c.New as the flag's value and records c in the audit trail.
	Put(ctx context.Context, c Change) error
}

// Status is a flag's definition with its current effective value.
type Status struct {
	Definition
	// Value is the effective value: the stored one, or the default.
	Value     string
	UpdatedBy string
	UpdatedAt time.Time
}

// Flags is the cache of flag values. Readers never block on the backend. A
// nil *Flags reports every flag's default.
type Flags struct {
	reg     *Registry
	backend Backend
	now     func() time.Time

	mu sync.RWMutex
	// values holds the last good load, plus write-through updates. Only
	// registered names with valid values are kept.
	values map[string]Value
}

// New returns a cache over backend, starting with every flag at its default.
// A nil backend means defaults only.
func New(reg *Registry, backend Backend) *Flags {
	return &Flags{reg: reg, backend: backend, now: time.Now, values: map[string]Value{}}
}

// raw returns the effective canonical value of the flag def.
func (f *Flags) raw(def Definition) string {
	if f == nil {
		return def.Default
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if v, ok := f.values[def.Name]; ok {
		return v.Raw
	}
	return def.Default
}

// Enabled returns the bool flag's current value. ctx is for scoped
// overrides later (per guild or channel); it is not used yet.
func (f *Flags) Enabled(_ context.Context, flag BoolFlag) bool {
	return f.raw(flag.def) == "true"
}

// Int returns the int flag's current value; see Enabled.
func (f *Flags) Int(_ context.Context, flag IntFlag) int64 {
	n, err := strconv.ParseInt(f.raw(flag.def), 10, 64)
	if err != nil {
		return flag.Default()
	}
	return n
}

// String returns the string flag's current value; see Enabled.
func (f *Flags) String(_ context.Context, flag StringFlag) string {
	return f.raw(flag.def)
}

// Refresh reloads every flag from the backend. On failure the cache keeps
// its current values and the error is returned. Unknown names and invalid
// values are logged and skipped, so those flags keep their defaults.
func (f *Flags) Refresh(ctx context.Context) error {
	if f.backend == nil {
		return nil
	}
	stored, err := f.backend.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load flags: %w", err)
	}
	values := make(map[string]Value, len(stored))
	for name, v := range stored {
		def, ok := f.reg.Lookup(name)
		if !ok {
			slog.Default().DebugContext(ctx, "ignoring stored value of an unknown flag", slog.String("flag", name))
			continue
		}
		canonical, err := def.Normalize(v.Raw)
		if err != nil {
			slog.Default().WarnContext(ctx, "ignoring invalid stored flag value, using the default",
				slog.String("flag", name), slog.String("value", v.Raw), slog.String("default", def.Default))
			continue
		}
		v.Name, v.Raw = name, canonical
		values[name] = v
	}
	f.mu.Lock()
	f.values = values
	f.mu.Unlock()
	return nil
}

// Run loads the flags at once and then every interval until ctx is done.
// Failures are logged; the cache keeps its last known good values (or the
// defaults, if no load has worked yet) and the next tick retries.
func (f *Flags) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	f.run(ctx, t.C)
}

// refreshTimeout bounds one background load.
const refreshTimeout = 10 * time.Second

func (f *Flags) run(ctx context.Context, ticks <-chan time.Time) {
	for {
		loadCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
		if err := f.Refresh(loadCtx); err != nil && ctx.Err() == nil {
			slog.Default().ErrorContext(ctx, "failed to refresh feature flags, keeping the current values", slog.Any("error", err))
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

// Set validates value for the flag called name, stores it and updates the
// cache at once. by is the Discord user ID making the change. Setting the
// current value is a no-op that writes nothing; check Change.Unchanged.
func (f *Flags) Set(ctx context.Context, name, value, by string) (Change, error) {
	def, ok := f.reg.Lookup(name)
	if !ok {
		return Change{}, fmt.Errorf("%w: %q", ErrUnknownFlag, name)
	}
	canonical, err := def.Normalize(value)
	if err != nil {
		return Change{}, err
	}
	c := Change{Name: name, Old: f.raw(def), New: canonical, By: by, At: f.now().UTC()}
	if c.Unchanged() {
		return c, nil
	}
	if f.backend == nil {
		return Change{}, ErrNoBackend
	}
	if err := f.backend.Put(ctx, c); err != nil {
		return Change{}, fmt.Errorf("store flag %s: %w", name, err)
	}
	f.mu.Lock()
	f.values[name] = Value{Name: name, Raw: canonical, UpdatedBy: by, UpdatedAt: c.At, PrevValue: c.Old}
	f.mu.Unlock()
	return c, nil
}

// List returns every registered flag with its effective value, sorted by name.
func (f *Flags) List() []Status {
	defs := f.reg.Definitions()
	out := make([]Status, 0, len(defs))
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, d := range defs {
		s := Status{Definition: d, Value: d.Default}
		if v, ok := f.values[d.Name]; ok {
			s.Value, s.UpdatedBy, s.UpdatedAt = v.Raw, v.UpdatedBy, v.UpdatedAt
		}
		out = append(out, s)
	}
	return out
}
