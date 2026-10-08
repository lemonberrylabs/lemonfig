package lemonfig_test

import (
	"context"
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
			if !errors.Is(err, lemonfig.ErrSecretTag) {
				t.Fatalf("err = %v, want ErrSecretTag", err)
			}
			var tagErr lemonfig.SecretTagError
			if !errors.As(err, &tagErr) || tagErr.Path != tt.wantPath || tagErr.Name != "A" {
				t.Errorf("SecretTagError = %+v, want path %q name A (err: %v)", tagErr, tt.wantPath, err)
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
	if err := mgr.Reload(context.Background()); !errors.Is(err, lemonfig.ErrSecretTag) {
		t.Fatalf("err = %v, want ErrSecretTag", err)
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
	if seen[0] != "[REDACTED]" {
		t.Errorf("GetString = %q, want [REDACTED]", seen[0])
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
		if err := mgr.Start(context.Background()); !errors.Is(err, lemonfig.ErrSecretTag) {
			t.Errorf("err = %v, want ErrSecretTag", err)
		}
	})
	t.Run("no target reads the path", func(t *testing.T) {
		t.Parallel()
		mgr, _ := startWithSource(t, doc, lemonfig.WithSecretResolver(newSecretStore("A", "va").resolve))
		lemonfig.Key[string](mgr, "db.host")
		if err := mgr.Start(context.Background()); !errors.Is(err, lemonfig.ErrSecretTag) {
			t.Errorf("err = %v, want ErrSecretTag", err)
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

// With a resolver configured, a document is decoded by lemonfig rather than
// by Viper's reader. A document without tags must load identically.
func TestSecretResolver_UntaggedDocumentLoadsIdentically(t *testing.T) {
	t.Parallel()
	const doc = `
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
`
	settings := func(opts ...lemonfig.Option) map[string]any {
		var got map[string]any
		opts = append(opts, lemonfig.WithValidation(func(v *viper.Viper) error {
			got = v.AllSettings()
			return nil
		}))
		mgr, _ := startWithSource(t, doc, opts...)
		mustStart(t, mgr)
		return got
	}
	plain := settings()
	withResolver := settings(lemonfig.WithSecretResolver(newSecretStore().resolve))
	if len(plain) == 0 || !reflect.DeepEqual(plain, withResolver) {
		t.Errorf("settings differ\nviper:    %#v\nresolver: %#v", plain, withResolver)
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

func TestCheckSecretTags(t *testing.T) {
	t.Parallel()
	t.Run("valid document", func(t *testing.T) {
		t.Parallel()
		bad, err := lemonfig.CheckSecretTags[taggedConfig]([]byte(
			"api_key: !secret A\nproviders:\n  - api_key: !secret B\n  - api_key: literal\ntoken: !secret C\ndisplay_name: x"))
		if err != nil || len(bad) != 0 {
			t.Errorf("bad = %v, err = %v", bad, err)
		}
	})
	t.Run("reports every violation", func(t *testing.T) {
		t.Parallel()
		bad, err := lemonfig.CheckSecretTags[taggedConfig]([]byte(`
api_key: !secret OK
display_name: !secret A
url: !secret B
providers:
  - {name: !secret C, api_key: !secret OK}
extra: !secret D
`))
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
			if _, err := lemonfig.CheckSecretTags[taggedConfig]([]byte(doc)); !errors.Is(err, lemonfig.ErrParseFailed) {
				t.Errorf("%q: err = %v, want ErrParseFailed", doc, err)
			}
		}
	})
	t.Run("empty document", func(t *testing.T) {
		t.Parallel()
		if bad, err := lemonfig.CheckSecretTags[taggedConfig](nil); err != nil || len(bad) != 0 {
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
