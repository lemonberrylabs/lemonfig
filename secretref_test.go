package lemonfig_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemonberrylabs/lemonfig"
	"github.com/lemonberrylabs/lemonfig/source"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// secretStore is a SecretResolver backed by a map, recording every call.
type secretStore struct {
	mu     sync.Mutex
	values map[string]string
	err    error
	calls  []string
}

func newSecretStore(kv ...string) *secretStore {
	s := &secretStore{values: map[string]string{}}
	for i := 0; i < len(kv); i += 2 {
		s.values[kv[i]] = kv[i+1]
	}
	return s
}

func (s *secretStore) resolve(_ context.Context, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	if s.err != nil {
		return "", s.err
	}
	v, ok := s.values[name]
	if !ok {
		return "", fmt.Errorf("no secret %q", name)
	}
	return v, nil
}

func (s *secretStore) set(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[name] = value
}

func (s *secretStore) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *secretStore) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// recLogger records Error calls.
type recLogger struct {
	mu   sync.Mutex
	errs []string
}

func (l *recLogger) Info(string, ...any) {}

func (l *recLogger) Error(msg string, kv ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errs = append(l.errs, fmt.Sprint(append([]any{msg}, kv...)...))
}

func (l *recLogger) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

type provider struct {
	Name   string          `mapstructure:"name"`
	APIKey lemonfig.Secret `mapstructure:"api_key"`
}

type shared struct {
	Token lemonfig.Secret `mapstructure:"token"`
}

type taggedConfig struct {
	DisplayName string              `mapstructure:"display_name"`
	URL         string              `mapstructure:"url"`
	APIKey      lemonfig.Secret     `mapstructure:"api_key"`
	Providers   []provider          `mapstructure:"providers"`
	ByName      map[string]provider `mapstructure:"by_name"`
	Ptr         *provider           `mapstructure:"ptr"`
	Extra       any                 `mapstructure:"extra"`
	Labels      map[string]string   `mapstructure:"labels"`
	shared      `mapstructure:",squash"`
	Skipped     lemonfig.Secret `mapstructure:"-"`
}

func TestSecretResolver_ResolvesOntoSecretFields(t *testing.T) {
	t.Parallel()
	store := newSecretStore("A", "va", "B", "vb", "C", "vc")
	mgr, _ := startWithSource(t, `
display_name: plain
API_KEY: !secret A
providers:
  - name: p0
    api_key: literal
  - name: p1
    api_key: !secret B
by_name:
  x: {name: px, api_key: !secret C}
ptr: {api_key: !secret A}
token: !secret B
`, lemonfig.WithSecretResolver(store.resolve))
	cfg := lemonfig.Load[taggedConfig](mgr)
	mustStart(t, mgr)

	c := cfg.Get()
	got := map[string]string{
		"api_key":              c.APIKey.Reveal(),
		"providers[0].api_key": c.Providers[0].APIKey.Reveal(),
		"providers[1].api_key": c.Providers[1].APIKey.Reveal(),
		"by_name.x.api_key":    c.ByName["x"].APIKey.Reveal(),
		"ptr.api_key":          c.Ptr.APIKey.Reveal(),
		"token":                c.Token.Reveal(),
	}
	want := map[string]string{
		"api_key": "va", "providers[0].api_key": "literal", "providers[1].api_key": "vb",
		"by_name.x.api_key": "vc", "ptr.api_key": "va", "token": "vb",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if c.DisplayName != "plain" || c.Providers[1].Name != "p1" {
		t.Errorf("plain fields lost: %+v", c)
	}
	// One call per distinct name per load.
	if calls := store.called(); len(calls) != 3 {
		t.Errorf("resolver calls = %v, want one each for A, B, C", calls)
	}
}

// The rule that fixes the leak: a tag that does not land on a Secret field
// rejects the generation, names the path, and resolves nothing.
func TestSecretResolver_RejectsTagOffSecretField(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		yaml     string
		wantPath string
	}{
		{"string field", "display_name: !secret A", "display_name"},
		{"string field next to a valid tag", "api_key: !secret A\nurl: !secret A", "url"},
		{"string in slice of structs", "providers:\n  - name: !secret A", "providers[0].name"},
		{"string in map of structs", "by_name:\n  x: {name: !secret A}", "by_name.x.name"},
		{"map of strings", "labels: {k: !secret A}", "labels.k"},
		{"any field", "extra: !secret A", "extra"},
		{"inside any field", "extra: {deep: [!secret A]}", "extra.deep[0]"},
		{"undeclared key", "nope: !secret A", "nope"},
		{"field excluded from decoding", "skipped: !secret A", "skipped"},
		{"struct where a secret is tagged", "providers: !secret A", "providers"},
		{"alias carries the tag to a string", "api_key: &k !secret A\ndisplay_name: *k", "display_name"},
		{"merge key carries the tag to a string", "ptr: &p {api_key: !secret A}\nlabels:\n  <<: *p", "labels.api_key"},
		{"quoted tagged scalar", `display_name: !secret "A"`, "display_name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newSecretStore("A", "va")
			log := &recLogger{}
			mgr, _ := startWithSource(t, tt.yaml, lemonfig.WithSecretResolver(store.resolve), lemonfig.WithLogger(log))
			lemonfig.Load[taggedConfig](mgr)

			err := mgr.Start(context.Background())
			if err == nil {
				mgr.Stop()
				t.Fatal("generation accepted")
			}
			if !errors.Is(err, lemonfig.ErrSecretRef) {
				t.Fatalf("err = %v, want ErrSecretRef", err)
			}
			var tagErr lemonfig.SecretRefError
			if !errors.As(err, &tagErr) || tagErr.Path != tt.wantPath || tagErr.Name != "A" {
				t.Errorf("SecretRefError = %+v, want path %q name A (err: %v)", tagErr, tt.wantPath, err)
			}
			if calls := store.called(); len(calls) != 0 {
				t.Errorf("resolver was called for a rejected document: %v", calls)
			}
			if !log.contains(tt.wantPath) {
				t.Errorf("rejection not logged with the path: %v", log.errs)
			}
		})
	}
}

func TestSecretResolver_MalformedTags(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"tag on mapping":  "ptr: !secret {api_key: x}",
		"tag on sequence": "providers: !secret [a]",
		"tag on key":      "? !secret A\n: value",
		"empty name":      "api_key: !secret ''",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newSecretStore("A", "va")
			mgr, _ := startWithSource(t, doc, lemonfig.WithSecretResolver(store.resolve))
			lemonfig.Load[taggedConfig](mgr)
			err := mgr.Start(context.Background())
			if err == nil {
				mgr.Stop()
				t.Fatal("generation accepted")
			}
			if !errors.Is(err, lemonfig.ErrParseFailed) {
				t.Errorf("err = %v, want ErrParseFailed", err)
			}
			if calls := store.called(); len(calls) != 0 {
				t.Errorf("resolver was called: %v", calls)
			}
		})
	}
}

// A rejected reload keeps the previous generation.
func TestSecretResolver_RejectedReloadKeepsGeneration(t *testing.T) {
	t.Parallel()
	store := newSecretStore("A", "va")
	mgr, src := startWithSource(t, "api_key: !secret A\ndisplay_name: ok", lemonfig.WithSecretResolver(store.resolve))
	cfg := lemonfig.Load[taggedConfig](mgr)
	mustStart(t, mgr)

	src.Set("api_key: !secret A\ndisplay_name: !secret A")
	if err := mgr.Reload(context.Background()); !errors.Is(err, lemonfig.ErrSecretRef) {
		t.Fatalf("err = %v, want ErrSecretRef", err)
	}
	if c := cfg.Get(); c.DisplayName != "ok" || c.APIKey.Reveal() != "va" {
		t.Errorf("generation changed after a rejected reload: %+v", c)
	}
}

// The resolved value is a Secret inside Viper too, so no read of the Viper
// instance (validation, OnReload, a string getter) returns the plaintext.
func TestSecretResolver_ViperNeverHoldsPlaintext(t *testing.T) {
	t.Parallel()
	const value = "resolved-plaintext"
	store := newSecretStore("A", value)
	var seen []string
	inspect := func(v *viper.Viper) error {
		seen = append(seen,
			v.GetString("api_key"),
			fmt.Sprint(v.Get("api_key")),
			fmt.Sprintf("%v %+v %#v", v.AllSettings(), v.AllSettings(), v.AllSettings()),
		)
		return nil
	}
	mgr, _ := startWithSource(t, "api_key: !secret A\nproviders:\n  - api_key: !secret A",
		lemonfig.WithSecretResolver(store.resolve),
		lemonfig.WithValidation(inspect),
		lemonfig.WithOnReload(func(_, v *viper.Viper) { _ = inspect(v) }),
	)
	cfg := lemonfig.Load[taggedConfig](mgr)
	mustStart(t, mgr)
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if cfg.Get().APIKey.Reveal() != value {
		t.Fatal("secret not resolved")
	}
	if len(seen) != 9 { // validation on both loads, OnReload on the second
		t.Fatalf("inspected %d reads, want 9", len(seen))
	}
	for _, s := range seen {
		if strings.Contains(s, value) {
			t.Errorf("Viper returned the plaintext: %s", s)
		}
	}
	if seen[0] != "!secret A" {
		t.Errorf("GetString = %q, want the tag it was resolved from", seen[0])
	}
}

func TestSecretResolver_KeyAndStructTargets(t *testing.T) {
	t.Parallel()
	const doc = "db:\n  password: !secret A\n  host: h"
	type db struct {
		Host     string          `mapstructure:"host"`
		Password lemonfig.Secret `mapstructure:"password"`
	}

	t.Run("Struct and Key[Secret] accept", func(t *testing.T) {
		t.Parallel()
		mgr, _ := startWithSource(t, doc, lemonfig.WithSecretResolver(newSecretStore("A", "va").resolve))
		d := lemonfig.Struct[db](mgr, "db")
		k := lemonfig.Key[lemonfig.Secret](mgr, "db.password")
		mustStart(t, mgr)
		if d.Get().Password.Reveal() != "va" || k.Get().Reveal() != "va" || d.Get().Host != "h" {
			t.Errorf("struct %+v key %q", d.Get(), k.Get().Reveal())
		}
	})
	t.Run("Key[string] on the tagged path rejects", func(t *testing.T) {
		t.Parallel()
		mgr, _ := startWithSource(t, doc, lemonfig.WithSecretResolver(newSecretStore("A", "va").resolve))
		lemonfig.Struct[db](mgr, "db")
		lemonfig.Key[string](mgr, "db.password")
		if err := mgr.Start(context.Background()); !errors.Is(err, lemonfig.ErrSecretRef) {
			t.Errorf("err = %v, want ErrSecretRef", err)
		}
	})
	t.Run("no target reads the path", func(t *testing.T) {
		t.Parallel()
		mgr, _ := startWithSource(t, doc, lemonfig.WithSecretResolver(newSecretStore("A", "va").resolve))
		lemonfig.Key[string](mgr, "db.host")
		if err := mgr.Start(context.Background()); !errors.Is(err, lemonfig.ErrSecretRef) {
			t.Errorf("err = %v, want ErrSecretRef", err)
		}
	})
}

func TestSecretResolver_ResolverError(t *testing.T) {
	t.Parallel()
	store := newSecretStore("A", "va")
	log := &recLogger{}
	mgr, _ := startWithSource(t, "api_key: !secret A",
		lemonfig.WithSecretResolver(store.resolve), lemonfig.WithLogger(log))
	cfg := lemonfig.Load[taggedConfig](mgr)
	mustStart(t, mgr)

	store.fail(errors.New("backend down"))
	err := mgr.Reload(context.Background())
	if !errors.Is(err, lemonfig.ErrSecretResolveFailed) {
		t.Fatalf("err = %v, want ErrSecretResolveFailed", err)
	}
	for _, want := range []string{"A", "api_key", "backend down"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if !log.contains("backend down") {
		t.Errorf("resolver error not logged: %v", log.errs)
	}
	if cfg.Get().APIKey.Reveal() != "va" {
		t.Error("generation changed after a failed resolution")
	}
}

// A secret resolved from a tag prints and marshals as that tag, so a config
// dumped as YAML and written back resolves the secret again instead of
// storing a stand-in as its value.
func TestSecretResolver_TagRoundTrips(t *testing.T) {
	t.Parallel()
	type dumpable struct {
		Name   string          `mapstructure:"name" yaml:"name" json:"name"`
		APIKey lemonfig.Secret `mapstructure:"api_key" yaml:"api_key" json:"api_key"`
		Local  lemonfig.Secret `mapstructure:"local" yaml:"local" json:"local"`
	}
	const value = "resolved-plaintext"
	store := newSecretStore("A", value)
	mgr, src := startWithSource(t, "name: n\napi_key: !secret A\nlocal: lit", lemonfig.WithSecretResolver(store.resolve))
	cfg := lemonfig.Load[dumpable](mgr)
	mustStart(t, mgr)
	loaded := cfg.Get()

	asYAML, err := yaml.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	asJSON, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	printed := fmt.Sprintf("%v %+v %#v %s", loaded, loaded, loaded, loaded.APIKey)
	for name, out := range map[string]string{"yaml": string(asYAML), "json": string(asJSON), "fmt": printed} {
		if strings.Contains(out, value) || strings.Contains(out, "lit") {
			t.Errorf("%s output leaks a plaintext: %s", name, out)
		}
	}
	if want := "name: \"n\"\napi_key: '!secret A'\nlocal: '[REDACTED]'\n"; string(asYAML) != want {
		t.Errorf("yaml = %q, want %q", asYAML, want)
	}
	if want := `{"name":"n","api_key":"!secret A","local":"[REDACTED]"}`; string(asJSON) != want {
		t.Errorf("json = %s, want %s", asJSON, want)
	}
	if got := loaded.APIKey.String(); got != "!secret A" {
		t.Errorf("String() = %q, want %q", got, "!secret A")
	}

	// Each dump, written back with the literal secret restored, resolves the
	// reference again and picks up the current value.
	for i, doc := range []string{
		strings.Replace(string(asYAML), "'[REDACTED]'", "lit", 1),
		strings.Replace(string(asJSON), "[REDACTED]", "lit", 1),
	} {
		rotated := fmt.Sprintf("rotated-%d", i)
		store.set("A", rotated)
		before := len(store.called())
		src.Set(doc)
		if err := mgr.Reload(context.Background()); err != nil {
			t.Fatalf("write-back %d: %v", i, err)
		}
		if got := cfg.Get().APIKey.Reveal(); got != rotated {
			t.Errorf("write-back %d: key = %q, want %q", i, got, rotated)
		}
		if len(store.called()) != before+1 {
			t.Errorf("write-back %d did not resolve the secret again", i)
		}
	}

	// A dump written back unedited still carries the [REDACTED] marker for
	// the literal secret and is rejected.
	src.Set(string(asYAML))
	if err := mgr.Reload(context.Background()); err == nil || !strings.Contains(err.Error(), "redaction marker") {
		t.Errorf("err = %v, want the redaction-marker error", err)
	}
}

// One reference syntax in every format: the string "!secret NAME".
func TestSecretResolver_StringReferences(t *testing.T) {
	t.Parallel()
	docs := map[string]struct{ ok, onString string }{
		"yaml": {"api_key: '!secret A'\nproviders:\n  - api_key: \"!secret   B  \"", "display_name: '!secret A'"},
		"json": {`{"api_key": "!secret A", "providers": [{"api_key": "!secret B"}]}`, `{"display_name": "!secret A"}`},
		"toml": {"api_key = '!secret A'\n[[providers]]\napi_key = '!secret B'", "display_name = '!secret A'"},
	}
	for format, doc := range docs {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			store := newSecretStore("A", "va", "B", "vb")
			mgr, _ := startWithSource(t, doc.ok, lemonfig.WithConfigType(format), lemonfig.WithSecretResolver(store.resolve))
			cfg := lemonfig.Load[taggedConfig](mgr)
			mustStart(t, mgr)
			if c := cfg.Get(); c.APIKey.Reveal() != "va" || c.Providers[0].APIKey.Reveal() != "vb" {
				t.Errorf("resolved %q and %q", c.APIKey.Reveal(), c.Providers[0].APIKey.Reveal())
			}

			store = newSecretStore("A", "va")
			bad, _ := startWithSource(t, doc.onString, lemonfig.WithConfigType(format), lemonfig.WithSecretResolver(store.resolve))
			lemonfig.Load[taggedConfig](bad)
			err := bad.Start(context.Background())
			var refErr lemonfig.SecretRefError
			if !errors.Is(err, lemonfig.ErrSecretRef) || !errors.As(err, &refErr) || refErr.Path != "display_name" {
				t.Errorf("reference on a string field: err = %v", err)
			}
			if calls := store.called(); len(calls) != 0 {
				t.Errorf("resolver was called for a rejected document: %v", calls)
			}

			violations, err := lemonfig.CheckSecretRefs[taggedConfig]([]byte(doc.onString), format)
			if err != nil || len(violations) != 1 || violations[0].Path != "display_name" {
				t.Errorf("CheckSecretRefs = %v, %v", violations, err)
			}
			if violations, err := lemonfig.CheckSecretRefs[taggedConfig]([]byte(doc.ok), format); err != nil || len(violations) != 0 {
				t.Errorf("CheckSecretRefs on the valid document = %v, %v", violations, err)
			}
		})
	}
}

// "!!secret ..." is the literal string "!secret ...": one "!" is removed and
// nothing is resolved. Strings that only resemble a reference are untouched.
func TestSecretResolver_Escape(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"!!secret NAME":  "!secret NAME",
		"!!!secret NAME": "!!secret NAME",
		"!!secret":       "!secret",
		"!!other":        "!!other",
		"!!secretive":    "!!secretive",
		"!secretive":     "!secretive",
		"! secret NAME":  "! secret NAME",
		"x !secret NAME": "x !secret NAME",
	}
	for in, want := range tests {
		for _, format := range []string{"yaml", "json"} {
			t.Run(format+" "+in, func(t *testing.T) {
				t.Parallel()
				doc, err := json.Marshal(map[string]any{"display_name": in, "labels": map[string]string{"k": in}, "extra": []string{in}})
				if err != nil {
					t.Fatal(err)
				}
				store := newSecretStore("NAME", "resolved")
				mgr, _ := startWithSource(t, string(doc), lemonfig.WithConfigType(format), lemonfig.WithSecretResolver(store.resolve))
				cfg := lemonfig.Load[taggedConfig](mgr)
				mustStart(t, mgr)
				c := cfg.Get()
				if c.DisplayName != want || c.Labels["k"] != want || !reflect.DeepEqual(c.Extra, []any{want}) {
					t.Errorf("got %q, %q, %v; want %q", c.DisplayName, c.Labels["k"], c.Extra, want)
				}
				if calls := store.called(); len(calls) != 0 {
					t.Errorf("resolver was called: %v", calls)
				}
			})
		}
	}
}

func TestSecretResolver_ReferenceWithoutResolver(t *testing.T) {
	t.Parallel()
	mgr, _ := startWithSource(t, "api_key: !secret A")
	lemonfig.Load[taggedConfig](mgr)
	err := mgr.Start(context.Background())
	if !errors.Is(err, lemonfig.ErrSecretResolveFailed) || !strings.Contains(err.Error(), "no SecretResolver") {
		t.Errorf("err = %v, want ErrSecretResolveFailed naming the missing resolver", err)
	}
}

// A resolved value that is itself a reference fails; it is not followed.
func TestSecretResolver_ValueIsAReference(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"!secret B", "!secret A", "!secret"} {
		store := newSecretStore("A", value, "B", "vb")
		mgr, _ := startWithSource(t, "api_key: !secret A", lemonfig.WithSecretResolver(store.resolve))
		lemonfig.Load[taggedConfig](mgr)
		err := mgr.Start(context.Background())
		if !errors.Is(err, lemonfig.ErrSecretResolveFailed) || !strings.Contains(err.Error(), "itself a secret reference") {
			t.Errorf("value %q: err = %v", value, err)
		}
		if calls := store.called(); len(calls) != 1 {
			t.Errorf("value %q: resolver calls = %v, want only A", value, calls)
		}
	}
}

// Distinct names are resolved concurrently, at most 8 at a time.
func TestSecretResolver_ResolvesInParallel(t *testing.T) {
	t.Parallel()
	const names = 20
	var doc strings.Builder
	doc.WriteString("providers:\n")
	for i := range names {
		fmt.Fprintf(&doc, "  - api_key: !secret S%d\n", i)
	}
	var mu sync.Mutex
	running, peak := 0, 0
	eight := make(chan struct{}) // closed once 8 calls are in flight together
	resolve := func(ctx context.Context, name string) (string, error) {
		mu.Lock()
		running++
		peak = max(peak, running)
		if running == 8 && peak == 8 {
			select {
			case <-eight:
			default:
				close(eight)
			}
		}
		mu.Unlock()
		select {
		case <-eight:
		case <-time.After(3 * time.Second):
			return "", errors.New("calls did not run concurrently")
		}
		time.Sleep(time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return "v-" + name, nil
	}
	mgr, _ := startWithSource(t, doc.String(), lemonfig.WithSecretResolver(resolve))
	cfg := lemonfig.Load[taggedConfig](mgr)
	mustStart(t, mgr)

	for i, p := range cfg.Get().Providers {
		if want := fmt.Sprintf("v-S%d", i); p.APIKey.Reveal() != want {
			t.Errorf("providers[%d] = %q, want %q", i, p.APIKey.Reveal(), want)
		}
	}
	if peak != 8 {
		t.Errorf("peak concurrency = %d, want 8", peak)
	}
}

// One failing name fails the reload with that name's error and cancels the
// calls still running.
func TestSecretResolver_FailureCancelsOthers(t *testing.T) {
	t.Parallel()
	resolve := func(ctx context.Context, name string) (string, error) {
		if name == "BAD" {
			return "", errors.New("denied")
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	mgr, _ := startWithSource(t, "api_key: !secret GOOD\ntoken: !secret BAD\nptr: {api_key: !secret OTHER}",
		lemonfig.WithSecretResolver(resolve))
	lemonfig.Load[taggedConfig](mgr)
	err := mgr.Start(context.Background())
	if !errors.Is(err, lemonfig.ErrSecretResolveFailed) || !strings.Contains(err.Error(), "BAD") || !strings.Contains(err.Error(), "denied") {
		t.Errorf("err = %v, want BAD's failure", err)
	}
}

// The Manager decodes the document itself. A document without references
// must give Viper the same settings its own reader would.
func TestManager_DecodesLikeViper(t *testing.T) {
	t.Parallel()
	docs := map[string]string{
		"yaml": `
Name: MixedCase
port: 8080
ratio: 1.5
debug: true
nothing: ~
when: 2026-01-02
list: [a, 2, {k: v}]
nested: {a: {b: [1, 2]}, Upper: x}
base: &base {x: 1}
derived: {<<: *base, y: 2}
1: numeric-key
`,
		"json": `{"Name": "MixedCase", "port": 8080, "ratio": 1.5, "debug": true, "nothing": null,
			"list": ["a", 2, {"k": "v"}], "nested": {"a": {"b": [1, 2]}, "Upper": "x"}}`,
		"toml": "Name = 'MixedCase'\nport = 8080\nratio = 1.5\ndebug = true\nwhen = 2026-01-02T00:00:00Z\n" +
			"list = ['a', 'b']\n[nested]\nUpper = 'x'\n[nested.a]\nb = [1, 2]\n[[items]]\nk = 'v'\n",
	}
	for format, doc := range docs {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			direct := viper.New()
			direct.SetConfigType(format)
			if err := direct.ReadConfig(strings.NewReader(doc)); err != nil {
				t.Fatal(err)
			}
			want := direct.AllSettings()

			var got map[string]any
			mgr, _ := startWithSource(t, doc, lemonfig.WithConfigType(format), lemonfig.WithValidation(func(v *viper.Viper) error {
				got = v.AllSettings()
				for _, key := range direct.AllKeys() {
					if !reflect.DeepEqual(v.Get(key), direct.Get(key)) {
						t.Errorf("Get(%q) = %#v, want %#v", key, v.Get(key), direct.Get(key))
					}
				}
				return nil
			}))
			mustStart(t, mgr)
			if len(want) == 0 || !reflect.DeepEqual(got, want) {
				t.Errorf("settings differ\nviper:    %#v\nlemonfig: %#v", want, got)
			}
		})
	}
}

func TestSecretResolver_NonYAMLUntouched(t *testing.T) {
	t.Parallel()
	mgr, _ := startWithSource(t, `{"api_key": "k"}`,
		lemonfig.WithConfigType("json"), lemonfig.WithSecretResolver(newSecretStore().resolve))
	cfg := lemonfig.Load[taggedConfig](mgr)
	mustStart(t, mgr)
	if cfg.Get().APIKey.Reveal() != "k" {
		t.Errorf("got %q", cfg.Get().APIKey.Reveal())
	}
}

func TestCheckSecretRefs(t *testing.T) {
	t.Parallel()
	t.Run("valid document", func(t *testing.T) {
		t.Parallel()
		bad, err := lemonfig.CheckSecretRefs[taggedConfig]([]byte(
			"api_key: !secret A\nproviders:\n  - api_key: !secret B\n  - api_key: literal\ntoken: !secret C\ndisplay_name: x"), "yaml")
		if err != nil || len(bad) != 0 {
			t.Errorf("bad = %v, err = %v", bad, err)
		}
	})
	t.Run("reports every violation", func(t *testing.T) {
		t.Parallel()
		bad, err := lemonfig.CheckSecretRefs[taggedConfig]([]byte(`
api_key: !secret OK
display_name: !secret A
url: !secret B
providers:
  - {name: !secret C, api_key: !secret OK}
extra: !secret D
`), "yaml")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, b := range bad {
			got[b.Path] = b.Name
			if b.Reason == "" || !strings.Contains(b.Error(), b.Path) {
				t.Errorf("violation without reason or path: %+v", b)
			}
		}
		want := map[string]string{"display_name": "A", "url": "B", "providers[0].name": "C", "extra": "D"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("violations = %v, want %v", got, want)
		}
	})
	t.Run("invalid document", func(t *testing.T) {
		t.Parallel()
		for _, doc := range []string{"a: [", "ptr: !secret {a: b}", "api_key: !secret ''"} {
			if _, err := lemonfig.CheckSecretRefs[taggedConfig]([]byte(doc), "yaml"); !errors.Is(err, lemonfig.ErrParseFailed) {
				t.Errorf("%q: err = %v, want ErrParseFailed", doc, err)
			}
		}
	})
	t.Run("empty document", func(t *testing.T) {
		t.Parallel()
		if bad, err := lemonfig.CheckSecretRefs[taggedConfig](nil, "yaml"); err != nil || len(bad) != 0 {
			t.Errorf("bad = %v, err = %v", bad, err)
		}
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// With PollingSource, a secret that rotates while the document is unchanged
// produces a reload, an unchanged secret produces none, and a failed
// resolution is logged and retried on the next tick.
func TestSecretResolver_PollingRotation(t *testing.T) {
	t.Parallel()
	store := newSecretStore("A", "v1")
	log := &recLogger{}
	var mu sync.Mutex
	count := 0
	src := source.NewPollingSource(newStaticSource("api_key: !secret A"), 10*time.Millisecond)
	mgr, err := lemonfig.NewManager(src,
		lemonfig.WithSecretResolver(store.resolve),
		lemonfig.WithLogger(log),
		lemonfig.WithOnReload(func(_, _ *viper.Viper) {
			mu.Lock()
			defer mu.Unlock()
			count++
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := lemonfig.Load[taggedConfig](mgr)
	rebuilds := 0
	lemonfig.Map(cfg, func(c taggedConfig) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		rebuilds++
		return c.APIKey.Reveal(), nil
	})
	mustStart(t, mgr)
	reloadCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
	key := func() string { return cfg.Get().APIKey.Reveal() }

	// The watch applies once at setup; after that, unchanged secrets are quiet.
	eventually(t, "setup apply", func() bool { return reloadCount() == 1 })
	time.Sleep(80 * time.Millisecond)
	if n := reloadCount(); n != 1 {
		t.Fatalf("%d reloads with nothing changed, want 1", n)
	}

	store.set("A", "v2")
	eventually(t, "rotation to v2", func() bool { return key() == "v2" })
	if n := reloadCount(); n != 2 {
		t.Errorf("%d reloads after one rotation, want 2", n)
	}

	// A resolver outage keeps the generation and is logged; the rotation that
	// happened during it is applied once the resolver recovers.
	store.fail(errors.New("backend down"))
	eventually(t, "resolver error logged", func() bool { return log.contains("backend down") })
	store.set("A", "v3")
	if key() != "v2" {
		t.Errorf("key = %q during the outage, want v2", key())
	}
	store.fail(nil)
	eventually(t, "rotation to v3", func() bool { return key() == "v3" })

	mu.Lock()
	defer mu.Unlock()
	if rebuilds != 3 {
		t.Errorf("dependent built %d times, want 3 (v1, v2, v3)", rebuilds)
	}
}
