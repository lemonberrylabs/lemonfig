package lemonfig_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lemonberrylabs/lemonfig"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

const plaintext = "hunter2-plaintext"

type secretHolder struct {
	Name   string           `json:"name" yaml:"name"`
	Key    lemonfig.Secret  `json:"key" yaml:"key"`
	Ptr    *lemonfig.Secret `json:"ptr" yaml:"ptr"`
	hidden lemonfig.Secret
}

func newHolder() secretHolder {
	s := lemonfig.NewSecret(plaintext)
	return secretHolder{Name: "n", Key: s, Ptr: &s, hidden: s}
}

func TestSecret_Reveal(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"", "a", plaintext, "ünï-çødé\x00\n", strings.Repeat("x", 4096)} {
		if got := lemonfig.NewSecret(want).Reveal(); got != want {
			t.Errorf("Reveal() = %q, want %q", got, want)
		}
	}
}

func TestSecret_RedactsEveryRead(t *testing.T) {
	t.Parallel()
	s := lemonfig.NewSecret(plaintext)
	h := newHolder()

	jsonOf := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	yamlOf := func(v any) string {
		b, err := yaml.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	slogOf := func(newHandler func(*bytes.Buffer) slog.Handler, v any) string {
		var buf bytes.Buffer
		slog.New(newHandler(&buf)).Info("m", "v", v)
		return buf.String()
	}
	jsonHandler := func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) }
	textHandler := func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) }
	text, err := s.MarshalText()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		got  string
		want string // exact output; empty means only the leak check applies
	}{
		{"String", s.String(), "[REDACTED]"},
		{"GoString", s.GoString(), `lemonfig.Secret([REDACTED])`},
		{"%v", fmt.Sprintf("%v", s), "[REDACTED]"},
		{"%+v", fmt.Sprintf("%+v", s), "[REDACTED]"},
		{"%#v", fmt.Sprintf("%#v", s), `lemonfig.Secret([REDACTED])`},
		{"%s", fmt.Sprintf("%s", s), "[REDACTED]"},
		{"%q", fmt.Sprintf("%q", s), `"[REDACTED]"`},
		{"%x", fmt.Sprintf("%x", s), "[REDACTED]"},
		{"%d", fmt.Sprintf("%d", s), "[REDACTED]"},
		{"Sprint pointer", fmt.Sprint(&s), "[REDACTED]"},
		{"MarshalText", string(text), "[REDACTED]"},
		{"json", jsonOf(s), `"[REDACTED]"`},
		{"yaml", yamlOf(s), "'[REDACTED]'\n"},
		{"struct %v", fmt.Sprintf("%v", h), ""},
		{"struct %+v", fmt.Sprintf("%+v", h), ""},
		{"struct %#v", fmt.Sprintf("%#v", h), ""},
		{"struct pointer %+v", fmt.Sprintf("%+v", &h), ""},
		{"struct json", jsonOf(h), `{"name":"n","key":"[REDACTED]","ptr":"[REDACTED]"}`},
		{"struct yaml", yamlOf(h), "name: \"n\"\nkey: '[REDACTED]'\nptr: '[REDACTED]'\n"},
		{"map %v", fmt.Sprintf("%v", map[string]any{"k": s}), "map[k:[REDACTED]]"},
		{"slice %v", fmt.Sprintf("%v", []lemonfig.Secret{s}), "[[REDACTED]]"},
		{"slog json attr", slogOf(jsonHandler, s), ""},
		{"slog text attr", slogOf(textHandler, s), ""},
		{"slog json struct", slogOf(jsonHandler, h), ""},
		{"slog text struct", slogOf(textHandler, h), ""},
		{"error text", fmt.Errorf("bad key %v in %+v", s, h).Error(), ""},
	}
	for _, tt := range tests {
		if strings.Contains(tt.got, plaintext) {
			t.Errorf("%s leaks the plaintext: %s", tt.name, tt.got)
		}
		if tt.want != "" && tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	if got := slogOf(jsonHandler, s); !strings.Contains(got, `"v":"[REDACTED]"`) {
		t.Errorf("slog json attr = %s, want the redaction marker", got)
	}
}

// A reflection-based printer reads unexported fields directly and never calls
// a method. Walk the struct the same way and check no string in it is the
// plaintext or contains it.
func TestSecret_NoPlaintextReachableByReflection(t *testing.T) {
	t.Parallel()
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			if strings.Contains(v.String(), plaintext) {
				t.Errorf("plaintext reachable by reflection: %q", v.String())
			}
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(newHolder()))
}

func TestSecret_Empty(t *testing.T) {
	t.Parallel()
	var zero lemonfig.Secret
	empty := lemonfig.NewSecret("")
	if !zero.IsEmpty() || !empty.IsEmpty() {
		t.Error("zero value and NewSecret(\"\") must be empty")
	}
	if zero != empty {
		t.Error("NewSecret(\"\") must equal the zero value")
	}
	if lemonfig.NewSecret("x").IsEmpty() {
		t.Error("non-empty secret reports IsEmpty")
	}
	if got := fmt.Sprintf("%v|%s|%q", zero, zero, zero); got != `||""` {
		t.Errorf("empty secret prints %q, want %q", got, `||""`)
	}
	if b, _ := json.Marshal(zero); string(b) != `""` {
		t.Errorf("empty secret JSON = %s, want \"\"", b)
	}
}

func TestSecret_Equality(t *testing.T) {
	t.Parallel()
	type cfg struct {
		Key  lemonfig.Secret
		Keys map[string]lemonfig.Secret
	}
	mk := func(v string) cfg {
		return cfg{Key: lemonfig.NewSecret(v), Keys: map[string]lemonfig.Secret{"a": lemonfig.NewSecret(v)}}
	}
	if lemonfig.NewSecret("a") != lemonfig.NewSecret("a") {
		t.Error("same plaintext must be ==")
	}
	if lemonfig.NewSecret("a") == lemonfig.NewSecret("b") {
		t.Error("different plaintext must not be ==")
	}
	if !reflect.DeepEqual(mk("a"), mk("a")) {
		t.Error("same plaintext must be DeepEqual")
	}
	if reflect.DeepEqual(mk("a"), mk("b")) {
		t.Error("different plaintext must not be DeepEqual")
	}
	// Same length, same prefix: equality must not come from length alone.
	if lemonfig.NewSecret("aaaa1") == lemonfig.NewSecret("aaaa2") {
		t.Error("secrets differing in the last byte compare equal")
	}
}

type secretConfig struct {
	Name    string          `mapstructure:"name"`
	APIKey  lemonfig.Secret `mapstructure:"api_key"`
	Timeout time.Duration   `mapstructure:"timeout"`
	DB      struct {
		Password *lemonfig.Secret `mapstructure:"password"`
	} `mapstructure:"db"`
}

func TestSecret_LoadDecodesPlainStrings(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		env    string // value for APP_API_KEY; unset when empty
		want   string
		wantDB string
	}{
		{"literal", "api_key: abc\ndb:\n  password: pw", "", "abc", "pw"},
		{"empty literal", "api_key: ''", "", "", ""},
		{"absent", "name: x", "", "", ""},
		{"null", "api_key:", "", "", ""},
		{"env overrides empty literal", "api_key: ''", "from-env", "from-env", ""},
		{"env overrides literal", "api_key: abc", "from-env", "from-env", ""},
		{"numeric-looking string", `api_key: "12345"`, "", "12345", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("APP_API_KEY", tt.env)
			}
			mgr, _ := startWithSource(t, tt.yaml+"\ntimeout: 3s", lemonfig.WithViperConfigure(func(v *viper.Viper) {
				v.SetEnvPrefix("APP")
				v.AutomaticEnv()
			}))
			cfg := lemonfig.Load[secretConfig](mgr)
			mustStart(t, mgr)

			got := cfg.Get()
			if got.APIKey.Reveal() != tt.want {
				t.Errorf("APIKey = %q, want %q", got.APIKey.Reveal(), tt.want)
			}
			if got.APIKey.IsEmpty() != (tt.want == "") {
				t.Errorf("IsEmpty = %v for %q", got.APIKey.IsEmpty(), tt.want)
			}
			var db string
			if got.DB.Password != nil {
				db = got.DB.Password.Reveal()
			}
			if db != tt.wantDB {
				t.Errorf("DB.Password = %q, want %q", db, tt.wantDB)
			}
			if got.Timeout != 3*time.Second {
				t.Errorf("Timeout = %v: the default duration hook was lost", got.Timeout)
			}
		})
	}
}

func TestSecret_NonStringLiteralRejected(t *testing.T) {
	t.Parallel()
	mgr, _ := startWithSource(t, "api_key: 12345")
	lemonfig.Load[secretConfig](mgr)
	err := mgr.Start(context.Background())
	if err == nil {
		mgr.Stop()
		t.Fatal("a YAML number decoded into a Secret")
	}
	if strings.Contains(err.Error(), "12345") {
		t.Errorf("the error repeats the value: %v", err)
	}
}

// A config dumped through a Secret's redacting marshallers and written back
// must not turn the marker into the secret's value.
func TestSecret_RedactionMarkerRejected(t *testing.T) {
	t.Parallel()
	dumped, err := yaml.Marshal(map[string]any{"api_key": lemonfig.NewSecret("real")})
	if err != nil {
		t.Fatal(err)
	}
	mgr, src := startWithSource(t, "api_key: real")
	cfg := lemonfig.Load[secretConfig](mgr)
	mustStart(t, mgr)

	src.Set(string(dumped))
	err = mgr.Reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redaction marker") || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err = %v, want a redaction-marker error naming api_key", err)
	}
	if got := cfg.Get().APIKey.Reveal(); got != "real" {
		t.Errorf("secret = %q after the rejected reload, want it unchanged", got)
	}
}

func TestDecodeOption_ViperUnmarshal(t *testing.T) {
	t.Parallel()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader("api_key: abc\ntimeout: 2s\ndb:\n  password: pw")); err != nil {
		t.Fatal(err)
	}
	var cfg secretConfig
	if err := v.Unmarshal(&cfg, lemonfig.DecodeOption()); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey.Reveal() != "abc" || cfg.DB.Password.Reveal() != "pw" || cfg.Timeout != 2*time.Second {
		t.Errorf("decoded %+v (key %q)", cfg, cfg.APIKey.Reveal())
	}

	// A caller composing its own hooks uses the bare hook. It replaces Viper's
	// defaults, so this target has no duration field.
	var bare struct {
		APIKey lemonfig.Secret `mapstructure:"api_key"`
	}
	if err := v.Unmarshal(&bare, viper.DecodeHook(lemonfig.SecretDecodeHook())); err != nil {
		t.Fatal(err)
	}
	if bare.APIKey.Reveal() != "abc" {
		t.Errorf("bare hook decoded %q", bare.APIKey.Reveal())
	}
}

// A dependent of a secret recomputes when the secret changes and not when an
// unrelated key does.
func TestSecret_DependentsRecomputeOnlyOnChange(t *testing.T) {
	t.Parallel()
	mgr, src := startWithSource(t, "name: a\napi_key: k1")
	cfg := lemonfig.Load[secretConfig](mgr)
	key := lemonfig.Map(cfg, func(c secretConfig) (lemonfig.Secret, error) { return c.APIKey, nil })
	builds := 0
	client := lemonfig.Map(key, func(s lemonfig.Secret) (string, error) {
		builds++
		return "client:" + s.Reveal(), nil
	})
	mustStart(t, mgr)

	reload := func(doc string) {
		t.Helper()
		src.Set(doc)
		if err := mgr.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	reload("name: b\napi_key: k1")
	if builds != 1 {
		t.Errorf("unrelated edit rebuilt the dependent: %d builds", builds)
	}
	reload("name: b\napi_key: k2")
	if builds != 2 || client.Get() != "client:k2" {
		t.Errorf("rotation: %d builds, client %q", builds, client.Get())
	}
}

func TestTestVal_Secret(t *testing.T) {
	t.Parallel()
	cfg := lemonfig.TestVal(secretConfig{APIKey: lemonfig.NewSecret("k")})
	key := lemonfig.Map(cfg, func(c secretConfig) (string, error) { return c.APIKey.Reveal(), nil })
	if key.Get() != "k" {
		t.Errorf("got %q", key.Get())
	}
}
