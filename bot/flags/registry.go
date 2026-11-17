// Package flags holds the bot's remote-controllable feature flags: a typed
// registry of the flags that exist (in code), an in-memory cache of their
// current values, and a backend that stores them (DynamoDB).
//
// Request paths only read the cache, so they never wait on AWS. The cache
// starts with the registry's defaults, is refreshed in the background, and
// keeps its last known good values when a refresh fails. Every default must
// be today's behaviour, so the bot works without the backend.
package flags

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind is the type of a flag's value.
type Kind int

const (
	KindBool Kind = iota
	KindInt
	KindString
)

func (k Kind) String() string {
	switch k {
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	default:
		return "string"
	}
}

// Definition describes one registered flag. Default is in canonical form.
type Definition struct {
	Name        string
	Kind        Kind
	Default     string
	Description string
}

// Normalize parses raw as a value of d's kind and returns it in canonical
// form ("true"/"false" for bools, a base-10 integer for ints, trimmed text
// for strings).
func (d Definition) Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch d.Kind {
	case KindBool:
		switch strings.ToLower(raw) {
		case "true", "on", "1", "yes", "enabled":
			return "true", nil
		case "false", "off", "0", "no", "disabled":
			return "false", nil
		}
	case KindInt:
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return strconv.FormatInt(n, 10), nil
		}
	case KindString:
		if raw != "" {
			return raw, nil
		}
	}
	return "", fmt.Errorf("%w: %q is not a valid %s", ErrInvalidValue, raw, d.Kind)
}

// BoolFlag, IntFlag and StringFlag are typed handles to registered flags,
// so a call site can't read a flag as the wrong type.
type (
	BoolFlag   struct{ def Definition }
	IntFlag    struct{ def Definition }
	StringFlag struct{ def Definition }
)

func (f BoolFlag) Name() string      { return f.def.Name }
func (f BoolFlag) Default() bool     { return f.def.Default == "true" }
func (f IntFlag) Name() string       { return f.def.Name }
func (f IntFlag) Default() int64     { n, _ := strconv.ParseInt(f.def.Default, 10, 64); return n }
func (f StringFlag) Name() string    { return f.def.Name }
func (f StringFlag) Default() string { return f.def.Default }

// validName keeps names usable as storage keys: '#' is reserved for scoped
// overrides later (name#guild:<id>), and for audit keys (audit#name).
var validName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Registry is the set of flags the code knows. Register flags at package
// init; registering is not safe for concurrent use, reading is.
type Registry struct {
	defs map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{defs: map[string]Definition{}}
}

// Bool registers a bool flag. It panics on an invalid or duplicate name:
// registration is a programming error, found by the first test run.
func (r *Registry) Bool(name string, def bool, description string) BoolFlag {
	return BoolFlag{r.register(name, KindBool, strconv.FormatBool(def), description)}
}

// Int registers an int flag; see Bool.
func (r *Registry) Int(name string, def int64, description string) IntFlag {
	return IntFlag{r.register(name, KindInt, strconv.FormatInt(def, 10), description)}
}

// String registers a string flag; see Bool.
func (r *Registry) String(name, def, description string) StringFlag {
	return StringFlag{r.register(name, KindString, def, description)}
}

func (r *Registry) register(name string, kind Kind, def, description string) Definition {
	if !validName.MatchString(name) {
		panic(fmt.Sprintf("flags: invalid flag name %q", name))
	}
	if _, dup := r.defs[name]; dup {
		panic(fmt.Sprintf("flags: flag %q registered twice", name))
	}
	d := Definition{Name: name, Kind: kind, Default: def, Description: description}
	r.defs[name] = d
	return d
}

// Lookup returns the definition of the flag called name.
func (r *Registry) Lookup(name string) (Definition, bool) {
	d, ok := r.defs[name]
	return d, ok
}

// Definitions returns every registered flag, sorted by name.
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
