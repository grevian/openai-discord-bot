package flags

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// fakeBackend is an in-memory Backend.
type fakeBackend struct {
	mu      sync.Mutex
	values  map[string]Value
	loadErr error
	putErr  error
	loads   int
	puts    []Change
}

func newFakeBackend() *fakeBackend { return &fakeBackend{values: map[string]Value{}} }

func (f *fakeBackend) LoadAll(context.Context) (map[string]Value, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := make(map[string]Value, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}
	return out, nil
}

func (f *fakeBackend) Put(_ context.Context, c Change) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.puts = append(f.puts, c)
	f.values[c.Name] = Value{Name: c.Name, Raw: c.New, UpdatedBy: c.By, UpdatedAt: c.At, PrevValue: c.Old}
	return nil
}

func (f *fakeBackend) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads
}

func testRegistry() (*Registry, BoolFlag, BoolFlag, IntFlag, StringFlag) {
	r := NewRegistry()
	off := r.Bool("feature_off", false, "off by default")
	on := r.Bool("feature_on", true, "on by default")
	n := r.Int("cap", 20, "a number")
	s := r.String("mode", "plain", "a word")
	return r, off, on, n, s
}

func TestRegistryRejectsDuplicatesAndBadNames(t *testing.T) {
	for _, name := range []string{"", "Upper", "has space", "has#hash", "9starts_with_digit"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registering %q should panic", name)
				}
			}()
			NewRegistry().Bool(name, false, "")
		}()
	}
	defer func() {
		if recover() == nil {
			t.Error("a duplicate name should panic")
		}
	}()
	r := NewRegistry()
	r.Bool("dup", false, "")
	r.Bool("dup", true, "")
}

func TestNormalize(t *testing.T) {
	r, _, _, _, _ := testRegistry()
	cases := []struct {
		flag, in, want string
		ok             bool
	}{
		{"feature_off", "on", "true", true},
		{"feature_off", "TRUE", "true", true},
		{"feature_off", "1", "true", true},
		{"feature_off", "yes", "true", true},
		{"feature_off", "off", "false", true},
		{"feature_off", "0", "false", true},
		{"feature_off", "no", "false", true},
		{"feature_off", "maybe", "", false},
		{"cap", " 42 ", "42", true},
		{"cap", "-3", "-3", true},
		{"cap", "4.5", "", false},
		{"cap", "lots", "", false},
		{"mode", " fancy ", "fancy", true},
		{"mode", "", "", false},
	}
	for _, tc := range cases {
		def, ok := r.Lookup(tc.flag)
		if !ok {
			t.Fatalf("%s not registered", tc.flag)
		}
		got, err := def.Normalize(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("Normalize(%s, %q) = (%q, %v), want (%q, ok=%v)", tc.flag, tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestDefaultsWithoutBackend(t *testing.T) {
	r, off, on, n, s := testRegistry()
	f := New(r, nil)
	ctx := context.Background()
	if f.Enabled(ctx, off) || !f.Enabled(ctx, on) || f.Int(ctx, n) != 20 || f.String(ctx, s) != "plain" {
		t.Fatal("without a backend every flag must have its default")
	}
	if err := f.Refresh(ctx); err != nil {
		t.Fatalf("refresh without a backend: %v", err)
	}
	if _, err := f.Set(ctx, "feature_off", "on", "owner"); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("Set without a backend = %v, want ErrNoBackend", err)
	}
	if f.Enabled(ctx, off) {
		t.Fatal("a failed Set must not change the value")
	}
}

func TestNilFlagsUsesDefaults(t *testing.T) {
	_, off, on, n, s := testRegistry()
	var f *Flags
	ctx := context.Background()
	if f.Enabled(ctx, off) || !f.Enabled(ctx, on) || f.Int(ctx, n) != 20 || f.String(ctx, s) != "plain" {
		t.Fatal("a nil *Flags must report defaults")
	}
}

func TestRefreshLoadsStoredValues(t *testing.T) {
	r, off, on, n, s := testRegistry()
	b := newFakeBackend()
	b.values["feature_off"] = Value{Name: "feature_off", Raw: "true"}
	b.values["feature_on"] = Value{Name: "feature_on", Raw: "false"}
	b.values["cap"] = Value{Name: "cap", Raw: "7"}
	b.values["mode"] = Value{Name: "mode", Raw: "fancy"}
	f := New(r, b)
	ctx := context.Background()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.Enabled(ctx, off) || f.Enabled(ctx, on) || f.Int(ctx, n) != 7 || f.String(ctx, s) != "fancy" {
		t.Fatal("stored values must override defaults")
	}
}

func TestRefreshIgnoresUnknownAndInvalidValues(t *testing.T) {
	r, off, _, n, _ := testRegistry()
	b := newFakeBackend()
	b.values["feature_off"] = Value{Name: "feature_off", Raw: "banana"}
	b.values["cap"] = Value{Name: "cap", Raw: "many"}
	b.values["retired_flag"] = Value{Name: "retired_flag", Raw: "true"}
	f := New(r, b)
	ctx := context.Background()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Enabled(ctx, off) || f.Int(ctx, n) != 20 {
		t.Fatal("invalid stored values must fall back to the default")
	}
	for _, st := range f.List() {
		if st.Name == "retired_flag" {
			t.Fatal("unknown names must not be listed")
		}
		if st.Value != st.Default {
			t.Errorf("%s lists %q, want its default %q", st.Name, st.Value, st.Default)
		}
	}
}

func TestRefreshFailureKeepsLastKnownGood(t *testing.T) {
	r, off, _, _, _ := testRegistry()
	b := newFakeBackend()
	b.values["feature_off"] = Value{Name: "feature_off", Raw: "true"}
	f := New(r, b)
	ctx := context.Background()
	if err := f.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.loadErr = errors.New("table gone")
	b.mu.Unlock()
	if err := f.Refresh(ctx); err == nil {
		t.Fatal("a failed load must be reported")
	}
	if !f.Enabled(ctx, off) {
		t.Fatal("a failed refresh must keep the last known good values")
	}
}

func TestFirstLoadFailureUsesDefaults(t *testing.T) {
	r, off, on, _, _ := testRegistry()
	b := newFakeBackend()
	b.loadErr = errors.New("unreachable")
	f := New(r, b)
	ctx := context.Background()
	if err := f.Refresh(ctx); err == nil {
		t.Fatal("a failed load must be reported")
	}
	if f.Enabled(ctx, off) || !f.Enabled(ctx, on) {
		t.Fatal("without a successful load every flag must have its default")
	}
}

func TestSetValidatesWritesAndUpdatesCache(t *testing.T) {
	r, off, _, _, _ := testRegistry()
	b := newFakeBackend()
	f := New(r, b)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := f.Set(ctx, "no_such_flag", "on", "owner"); !errors.Is(err, ErrUnknownFlag) {
		t.Fatalf("unknown flag: err = %v", err)
	}
	if _, err := f.Set(ctx, "feature_off", "sideways", "owner"); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("bad value: err = %v", err)
	}
	if len(b.puts) != 0 {
		t.Fatalf("rejected changes must not be written, got %+v", b.puts)
	}

	c, err := f.Set(ctx, "feature_off", "on", "owner-id")
	if err != nil {
		t.Fatal(err)
	}
	want := Change{Name: "feature_off", Old: "false", New: "true", By: "owner-id", At: now}
	if c != want || len(b.puts) != 1 || b.puts[0] != want {
		t.Fatalf("change = %+v, written %+v, want %+v", c, b.puts, want)
	}
	if !f.Enabled(ctx, off) {
		t.Fatal("a successful Set must take effect at once (write-through)")
	}
	var st Status
	for _, s := range f.List() {
		if s.Name == "feature_off" {
			st = s
		}
	}
	if st.Value != "true" || st.Default != "false" || st.UpdatedBy != "owner-id" || !st.UpdatedAt.Equal(now) {
		t.Fatalf("status after Set = %+v", st)
	}
}

func TestSetSameValueDoesNotWrite(t *testing.T) {
	r, _, _, _, _ := testRegistry()
	b := newFakeBackend()
	f := New(r, b)
	c, err := f.Set(context.Background(), "feature_off", "off", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Unchanged() || len(b.puts) != 0 {
		t.Fatalf("setting the current value must be a no-op, got %+v / %+v", c, b.puts)
	}
}

func TestSetWriteFailureKeepsOldValue(t *testing.T) {
	r, off, _, _, _ := testRegistry()
	b := newFakeBackend()
	b.putErr = errors.New("throttled")
	f := New(r, b)
	ctx := context.Background()
	if _, err := f.Set(ctx, "feature_off", "on", "owner"); err == nil {
		t.Fatal("a failed write must be reported")
	}
	if f.Enabled(ctx, off) {
		t.Fatal("a failed write must not change the cached value")
	}
}

func TestListIsSortedAndComplete(t *testing.T) {
	r, _, _, _, _ := testRegistry()
	got := New(r, nil).List()
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	want := []string{"cap", "feature_off", "feature_on", "mode"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestRunRefreshesUntilCancelled(t *testing.T) {
	r, off, _, _, _ := testRegistry()
	b := newFakeBackend()
	f := New(r, b)
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.run(ctx, ticks)
	}()

	// The first load happens straight away, then once per tick.
	waitFor(t, func() bool { return b.loadCount() == 1 })
	b.mu.Lock()
	b.values["feature_off"] = Value{Name: "feature_off", Raw: "true"}
	b.mu.Unlock()
	ticks <- time.Now()
	waitFor(t, func() bool { return f.Enabled(context.Background(), off) })

	cancel()
	<-done
}

// waitFor polls cond briefly; the condition is driven by the test itself, so
// it holds within a few scheduler turns.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(time.Millisecond)
	}
}
