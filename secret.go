package lemonfig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sync"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

const redacted = "[REDACTED]"

// Secret is a string config value whose plaintext is reachable only through
// [Secret.Reveal]. Every other way of reading it — String, GoString, every
// fmt verb, JSON, text and YAML marshalling, slog — yields a stand-in:
//
//   - "!secret NAME" for a secret the [SecretResolver] resolved from that
//     reference. Written back into a document in any format, it is a
//     reference again, so a dumped config resolves the secret anew.
//   - "[REDACTED]" for any other non-empty secret (a literal, an environment
//     variable, [NewSecret]).
//   - "" for an empty secret that has no name.
//
// The plaintext is not stored in the struct. It is sealed with a key generated
// once per process, so a printer that reads unexported fields by reflection
// (go-spew, a test-failure dump, fmt on an unexported enclosing field) prints
// ciphertext. The ciphertext does reveal the secret's length.
//
// Sealing is deterministic within a process: two Secrets holding the same
// plaintext, resolved from the same name or both from no name, are equal
// under == and [reflect.DeepEqual], and different plaintexts are unequal.
// [Map] and [Combine] therefore recompute dependents when a secret rotates
// (or its tag is renamed) and not otherwise.
//
// A Secret field decodes from a plain string through [Load] and [Struct],
// whether the string comes from the document or an environment variable. The
// zero value is the empty secret.
type Secret struct {
	sealed string
	ref    string // the NAME of the `!secret NAME` tag it was resolved from, if any
}

type secretSeal struct {
	aead   cipher.AEAD
	macKey [sha256.Size]byte
}

var processSeal = sync.OnceValue(func() *secretSeal {
	var key [32 + sha256.Size]byte
	rand.Read(key[:])
	// Neither error is reachable: 32 bytes is a valid AES key size and AES
	// has the block size GCM requires.
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	s := &secretSeal{aead: aead}
	copy(s.macKey[:], key[32:])
	return s
})

// NewSecret returns a [Secret] holding value.
func NewSecret(value string) Secret {
	if value == "" {
		return Secret{}
	}
	s := processSeal()
	// The nonce is a keyed hash of the plaintext, so equal plaintexts seal to
	// equal ciphertexts and distinct plaintexts never share a nonce.
	mac := hmac.New(sha256.New, s.macKey[:])
	mac.Write([]byte(value))
	nonce := mac.Sum(nil)[:s.aead.NonceSize()]
	return Secret{sealed: string(s.aead.Seal(nonce, nonce, []byte(value), nil))}
}

// Reveal returns the plaintext. It is the only way to read it.
func (s Secret) Reveal() string {
	seal := processSeal()
	n := seal.aead.NonceSize()
	if len(s.sealed) < n {
		return ""
	}
	plain, err := seal.aead.Open(nil, []byte(s.sealed[:n]), []byte(s.sealed[n:]), nil)
	if err != nil {
		return ""
	}
	return string(plain)
}

// IsEmpty reports whether the secret holds the empty string.
func (s Secret) IsEmpty() bool { return s.sealed == "" }

// String returns the stand-in described on [Secret], never the plaintext.
func (s Secret) String() string {
	switch {
	case s.ref != "":
		return secretTag + " " + s.ref
	case s.IsEmpty():
		return ""
	}
	return redacted
}

// GoString implements [fmt.GoStringer] without the plaintext.
func (s Secret) GoString() string {
	return "lemonfig.Secret(" + s.String() + ")"
}

// Format implements [fmt.Formatter] so that no verb prints the plaintext.
func (s Secret) Format(f fmt.State, verb rune) {
	switch {
	case verb == 'v' && f.Flag('#'):
		io.WriteString(f, s.GoString())
	case verb == 'q':
		fmt.Fprintf(f, "%q", s.String())
	default:
		io.WriteString(f, s.String())
	}
}

// MarshalText implements [encoding.TextMarshaler] without the plaintext.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// MarshalJSON implements [json.Marshaler] without the plaintext.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// MarshalYAML implements the yaml.v3 Marshaler interface without the
// plaintext. It returns any because that interface requires it.
func (s Secret) MarshalYAML() (any, error) { return s.String(), nil }

// LogValue implements [slog.LogValuer] without the plaintext.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

var secretType = reflect.TypeFor[Secret]()

// SecretDecodeHook returns a mapstructure decode hook that decodes a string
// into a [Secret]. [Load] and [Struct] already apply it; use it, or
// [DecodeOption], when decoding the same struct outside a [Manager].
//
// Only strings decode: a YAML number or boolean on a Secret field is an error,
// so quote such values in the document. Two strings are errors too:
// "[REDACTED]", which is what a literal Secret prints as and so only appears
// when a dump is written back, and "!secret NAME", which is a reference that
// was not resolved (a [Manager] resolves references before decoding).
func SecretDecodeHook() mapstructure.DecodeHookFunc {
	return func(from, to reflect.Type, data any) (any, error) {
		if to != secretType {
			return data, nil
		}
		switch v := data.(type) {
		case Secret:
			return v, nil
		case string:
			// The output of a dump fed back in as config.
			if v == redacted {
				return nil, fmt.Errorf("lemonfig: the value is the redaction marker %s, not a secret", redacted)
			}
			if _, ok := secretRefName(v); ok {
				return nil, fmt.Errorf("lemonfig: the value is an unresolved %s reference", secretTag)
			}
			return NewSecret(v), nil
		}
		return nil, fmt.Errorf("lemonfig: cannot decode %s into Secret: the value must be a string", from)
	}
}

// DecodeOption returns the Viper decoder option that [Load] and [Struct] use:
// Viper's default hooks (string to time.Duration, comma-separated string to
// slice) followed by [SecretDecodeHook]. Pass it to viper.Unmarshal to decode
// a struct with [Secret] fields the way a [Manager] does.
func DecodeOption() viper.DecoderConfigOption {
	return viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
		SecretDecodeHook(),
	))
}
