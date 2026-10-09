package lemonfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

const secretTag = "!secret"

// SecretResolver returns the value of the secret called name. The Manager
// calls it from several goroutines at once, so it must be safe for
// concurrent use.
type SecretResolver func(ctx context.Context, name string) (string, error)

// maxConcurrentResolves bounds how many resolver calls one reload runs at once.
const maxConcurrentResolves = 8

// SecretRefError describes one secret reference that cannot be accepted.
type SecretRefError struct {
	Path   string // where the reference lands, e.g. "providers[1].api_key"
	Name   string // the secret name it refers to
	Reason string
}

func (e SecretRefError) Error() string {
	return fmt.Sprintf("%s: %s %s: %s", e.Path, secretTag, e.Name, e.Reason)
}

// secretRefName reports whether the string s is a secret reference,
// "!secret NAME", and returns NAME (empty when the name is missing).
func secretRefName(s string) (string, bool) {
	if s == secretTag {
		return "", true
	}
	rest, ok := strings.CutPrefix(s, secretTag+" ")
	return strings.TrimSpace(rest), ok
}

// unescapeSecretRef turns the escaped form "!!secret ..." into the literal
// string "!secret ...". Each extra leading "!" escapes one level.
func unescapeSecretRef(s string) (string, bool) {
	if !strings.HasPrefix(s, "!!") {
		return s, false
	}
	if _, ok := secretRefName("!" + strings.TrimLeft(s, "!")); !ok {
		return s, false
	}
	return s[1:], true
}

// pathSeg is one step of a document path: a mapping key or a sequence index.
type pathSeg struct {
	key     string
	index   int
	isIndex bool
}

func formatPath(path []pathSeg) string {
	var b strings.Builder
	for i, s := range path {
		switch {
		case s.isIndex:
			b.WriteString("[" + strconv.Itoa(s.index) + "]")
		case i > 0:
			b.WriteString("." + s.key)
		default:
			b.WriteString(s.key)
		}
	}
	return b.String()
}

// secretUse is one place in the decoded document where a reference lands.
// A YAML scalar under an anchor lands once per alias or merge that reaches it.
type secretUse struct {
	path []pathSeg
	name string
}

// secretDoc is a decoded config document and the secret references in it.
//
// References are located in the decoded tree, not in the source text, so a
// path is where the value really lands after YAML aliases and merge keys are
// applied. The tree is map[string]any because that is what the decoders
// produce and what Viper consumes.
type secretDoc struct {
	tree map[string]any
	uses []secretUse
}

// parseSecretDoc decodes a document of the given config type. A reference is
// a string "!secret NAME" in any format, or a YAML scalar tagged !secret.
func parseSecretDoc(data []byte, configType string) (*secretDoc, error) {
	d := &secretDoc{}
	if isYAML(configType) {
		var root yaml.Node
		if err := yaml.Unmarshal(data, &root); err != nil {
			return nil, err
		}
		if root.Kind != 0 { // not an empty document
			if err := tagsToStrings(&root); err != nil {
				return nil, err
			}
			if err := root.Decode(&d.tree); err != nil {
				return nil, err
			}
		}
	} else {
		// Let Viper decode every other format it supports.
		v := viper.New()
		v.SetConfigType(configType)
		if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
			return nil, err
		}
		d.tree = v.AllSettings()
	}
	err := d.walk(d.tree, nil, func(path []pathSeg, s string) (any, bool, error) {
		name, ok := secretRefName(s)
		if !ok {
			return nil, false, nil
		}
		if name == "" {
			return nil, false, fmt.Errorf("%s: %s needs a secret name", formatPath(path), secretTag)
		}
		d.uses = append(d.uses, secretUse{path: slices.Clone(path), name: name})
		return nil, false, nil
	})
	return d, err
}

// tagsToStrings rewrites every YAML scalar tagged !secret under n as the
// string form of the reference, so the rest of the code handles one form.
func tagsToStrings(n *yaml.Node) error {
	if n.Tag == secretTag {
		if n.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: %s must tag a scalar", n.Line, secretTag)
		}
		n.Tag = "!!str"
		n.Value = secretTag + " " + n.Value
		return nil
	}
	for _, c := range n.Content {
		if err := tagsToStrings(c); err != nil {
			return err
		}
	}
	return nil
}

// walk calls visit for every string value in v, in a stable order. When visit
// returns true the string is replaced by the returned value.
func (d *secretDoc) walk(v any, path []pathSeg, visit func([]pathSeg, string) (any, bool, error)) error {
	// child handles one element and reports its replacement, if any.
	child := func(seg pathSeg, c any) (any, bool, error) {
		p := append(path, seg)
		if s, ok := c.(string); ok {
			return visit(p, s)
		}
		return nil, false, d.walk(c, p, visit)
	}
	refKey := func(k string) error {
		if _, ok := secretRefName(k); ok {
			return fmt.Errorf("%s: a mapping key cannot be a secret reference (%q)", formatPath(path), k)
		}
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(t)) {
			if err := refKey(k); err != nil {
				return err
			}
			repl, replace, err := child(pathSeg{key: k}, t[k])
			if err != nil {
				return err
			}
			if replace {
				t[k] = repl
			}
		}
	case map[any]any: // yaml.v3 produces this for a mapping with a non-string key
		keys := make(map[string]any, len(t))
		for k := range t {
			keys[fmt.Sprint(k)] = k
		}
		for _, name := range slices.Sorted(maps.Keys(keys)) {
			if err := refKey(name); err != nil {
				return err
			}
			repl, replace, err := child(pathSeg{key: name}, t[keys[name]])
			if err != nil {
				return err
			}
			if replace {
				t[keys[name]] = repl
			}
		}
	case []any:
		for i, c := range t {
			repl, replace, err := child(pathSeg{index: i, isIndex: true}, c)
			if err != nil {
				return err
			}
			if replace {
				t[i] = repl
			}
		}
	}
	return nil
}

// fill replaces every reference with the Secret for its name and every
// escaped reference with its literal string.
func (d *secretDoc) fill(values map[string]Secret) {
	// walk fails only on errors parseSecretDoc already reported.
	_ = d.walk(d.tree, nil, func(_ []pathSeg, s string) (any, bool, error) {
		if name, ok := secretRefName(s); ok {
			return values[name], true, nil
		}
		lit, ok := unescapeSecretRef(s)
		return lit, ok, nil
	})
}

// secretTarget is a registered type that reads the config at path.
type secretTarget struct {
	path []string
	typ  reflect.Type
}

// covers reports whether the target reads the document path, and if so the
// part of the path inside the target's type.
func (t secretTarget) covers(path []pathSeg) ([]pathSeg, bool) {
	if len(t.path) > len(path) {
		return nil, false
	}
	for i, key := range t.path {
		if path[i].isIndex || !strings.EqualFold(path[i].key, key) {
			return nil, false
		}
	}
	return path[len(t.path):], true
}

// violations returns one error for every reference that does not land on
// a [Secret] in every target that reads its path, or that no target reads.
func (d *secretDoc) violations(targets []secretTarget) []SecretRefError {
	var out []SecretRefError
	for _, use := range d.uses {
		reason := "no Load or Struct target declares this path"
		for _, t := range targets {
			rest, ok := t.covers(use.path)
			if !ok {
				continue
			}
			if reason = secretFieldReason(t.typ, rest); reason != "" {
				break
			}
		}
		if reason != "" {
			out = append(out, SecretRefError{Path: formatPath(use.path), Name: use.name, Reason: reason})
		}
	}
	return out
}

// secretFieldReason returns "" when path, followed through t the way
// mapstructure decodes it, ends on a [Secret]; otherwise it says why not.
func secretFieldReason(t reflect.Type, path []pathSeg) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == secretType && len(path) == 0 {
		return ""
	}
	if len(path) == 0 {
		return fmt.Sprintf("the field has type %s, not lemonfig.Secret", t)
	}
	seg := path[0]
	switch t.Kind() {
	case reflect.Struct:
		if seg.isIndex {
			return fmt.Sprintf("the document has a list where %s is expected", t)
		}
		f, ok := fieldByKey(t, seg.key)
		if !ok {
			return fmt.Sprintf("%s has no field for key %q", t, seg.key)
		}
		return secretFieldReason(f, path[1:])
	case reflect.Map:
		if seg.isIndex {
			return fmt.Sprintf("the document has a list where %s is expected", t)
		}
		return secretFieldReason(t.Elem(), path[1:])
	case reflect.Slice, reflect.Array:
		if !seg.isIndex {
			return fmt.Sprintf("the document has a mapping where %s is expected", t)
		}
		return secretFieldReason(t.Elem(), path[1:])
	}
	return fmt.Sprintf("a value of type %s cannot be shown to hold a lemonfig.Secret", t)
}

// fieldByKey finds the type of the struct field that mapstructure decodes key
// into: the `mapstructure` tag name or the field name, compared without case,
// looking through squashed embedded structs.
func fieldByKey(t reflect.Type, key string) (reflect.Type, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous { // an embedded struct of unexported type can still be squashed
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("mapstructure"), ",")
		if name == "-" {
			continue
		}
		if slices.Contains(strings.Split(opts, ","), "squash") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				if found, ok := fieldByKey(ft, key); ok {
					return found, true
				}
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if strings.EqualFold(name, key) {
			return f.Type, true
		}
	}
	return nil, false
}

// CheckSecretRefs reports every secret reference in a document that does not
// land on a [Secret]-typed field of T, following `mapstructure` tags the way
// [Load] decodes. format is the config type ("yaml", "json", "toml"). It
// resolves nothing and needs no [Manager]; use it to reject a document before
// it is stored.
//
// A reference is accepted on a Secret field at any depth, including inside
// maps and slices of structs, and wherever a YAML alias or merge key carries
// it. It is reported on any other type (string, any, a key T does not
// declare).
//
// The error is non-nil when the document cannot be parsed, or when a
// reference has no name, is a mapping key, or is a !secret tag on a
// non-scalar.
func CheckSecretRefs[T any](doc []byte, format string) ([]SecretRefError, error) {
	d, err := parseSecretDoc(doc, format)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseFailed, err)
	}
	return d.violations([]secretTarget{{typ: reflect.TypeFor[T]()}}), nil
}

// DecodeUnresolved decodes a document into T the way a [Manager] with
// [Load] does, once, without fetching any secret. Use it where the secret
// store is out of reach or not wanted: validating a document in CI or before
// storing it, or reading the settings needed to build a [SecretResolver].
//
// Every check a Manager makes still applies, including the rule that a
// reference must land on a [Secret] field. Each reference decodes to an
// unresolved Secret: it prints as "!secret NAME", is not empty, reports
// [Secret.IsUnresolved], reveals the empty string, and is never equal to a
// resolved one.
//
// format is the config type ("yaml", "json", "toml"). Of the options,
// [WithViperConfigure], [WithValidation] and [WithLogger] apply; a resolver
// given with [WithSecretResolver] is not called.
//
// To load once with secrets resolved, use a Manager: create it, call [Load]
// and [Manager.Start], read the value, and call [Manager.Stop].
func DecodeUnresolved[T any](doc []byte, format string, opts ...Option) (T, error) {
	m, _ := NewManager(bytesSource{data: doc, format: format}, opts...)
	m.cfg.configType = format
	m.unresolved = true
	val := Struct[T](m, "")
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reloadLocked(context.Background()); err != nil {
		var zero T
		return zero, err
	}
	return val.Get(), nil
}

// bytesSource is a [ConfigSource] over a document already in memory.
type bytesSource struct {
	data   []byte
	format string
}

func (s bytesSource) Fetch(context.Context) ([]byte, string, error) { return s.data, s.format, nil }

func isYAML(configType string) bool { return configType == "yaml" || configType == "yml" }

func (m *Manager) secretTargets() []secretTarget {
	var targets []secretTarget
	for _, n := range m.roots {
		if r, ok := n.(interface{ secretTarget() secretTarget }); ok {
			targets = append(targets, r.secretTarget())
		}
	}
	return targets
}

// resolveSecrets calls the resolver once per distinct secret name in d, up to
// maxConcurrentResolves at a time. The first failure cancels the other calls
// and is the error returned.
func (m *Manager) resolveSecrets(ctx context.Context, d *secretDoc) (map[string]Secret, error) {
	var first []secretUse // first use of each distinct name
	seen := make(map[string]bool)
	for _, use := range d.uses {
		if !seen[use.name] {
			seen[use.name] = true
			first = append(first, use)
		}
	}
	if len(first) == 0 {
		return nil, nil
	}
	if m.unresolved {
		values := make(map[string]Secret, len(first))
		for _, use := range first {
			values[use.name] = Secret{ref: use.name, unresolved: true}
		}
		return values, nil
	}
	if m.cfg.secretResolver == nil {
		use := first[0]
		return nil, fmt.Errorf("%w: %s (at %s): no SecretResolver is configured",
			ErrSecretResolveFailed, use.name, formatPath(use.path))
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The first failure is the one reported; it cancels the remaining calls,
	// whose own errors are then only consequences of it.
	var failure error
	var failOnce sync.Once
	fail := func(use secretUse, reason string, err error) {
		failOnce.Do(func() {
			failure = fmt.Errorf("%w: %s (at %s): %s", ErrSecretResolveFailed, use.name, formatPath(use.path), reason)
			if err != nil {
				failure = fmt.Errorf("%w: %s (at %s): %w", ErrSecretResolveFailed, use.name, formatPath(use.path), err)
			}
			cancel()
		})
	}
	secrets := make([]Secret, len(first))
	slots := make(chan struct{}, maxConcurrentResolves)
	var wg sync.WaitGroup
	for i, use := range first {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			if err := ctx.Err(); err != nil {
				fail(use, "", err)
				return
			}
			v, err := m.cfg.secretResolver(ctx, use.name)
			if err != nil {
				fail(use, "", err)
				return
			}
			if _, ok := secretRefName(v); ok {
				// A reference is resolved once; a value that is itself a
				// reference is a mistake in the secret store, not a chain to follow.
				fail(use, "its value is itself a secret reference", nil)
				return
			}
			secrets[i] = NewSecret(v)
			secrets[i].ref = use.name
		})
	}
	wg.Wait()
	if failure != nil {
		return nil, failure
	}
	values := make(map[string]Secret, len(first))
	for i, use := range first {
		values[use.name] = secrets[i]
	}
	return values, nil
}

// readConfig reads a document into v with every secret reference resolved to
// a [Secret] value. Nothing is resolved unless every reference lands on a
// Secret field.
func (m *Manager) readConfig(ctx context.Context, v *viper.Viper, data []byte, configType string) error {
	d, err := parseSecretDoc(data, configType)
	if err != nil {
		m.cfg.logger.Error("parse failed", "error", err)
		return fmt.Errorf("%w: %w", ErrParseFailed, err)
	}
	if bad := d.violations(m.secretTargets()); len(bad) > 0 {
		errs := make([]error, len(bad))
		for i, e := range bad {
			errs[i] = e
		}
		err := fmt.Errorf("%w: %w", ErrSecretRef, errors.Join(errs...))
		m.cfg.logger.Error("secret reference rejected", "error", err)
		return err
	}
	values, err := m.resolveSecrets(ctx, d)
	if err != nil {
		m.cfg.logger.Error("secret resolution failed", "error", err)
		return err
	}
	d.fill(values)
	if err := v.MergeConfigMap(d.tree); err != nil {
		m.cfg.logger.Error("parse failed", "error", err)
		return fmt.Errorf("%w: %w", ErrParseFailed, err)
	}
	return nil
}

// secretChangeKey is the [ChangeKeyer] function a Manager installs: a digest
// of the document and of every secret it references, so a rotated secret
// reads as a change.
//
// A document the reload would reject (unparsable, or a reference on a
// non-Secret path) is keyed on its bytes alone and nothing is resolved: the
// reload then runs and reports the rejection.
func (m *Manager) secretChangeKey(ctx context.Context, data []byte, format string) ([]byte, error) {
	h := sha256.New()
	field := func(s string) {
		h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(s))))
		h.Write([]byte(s))
	}
	field(string(data))
	d, err := parseSecretDoc(data, m.configType(format))
	if err != nil || len(d.violations(m.secretTargets())) > 0 {
		return h.Sum(nil), nil
	}
	values, err := m.resolveSecrets(ctx, d)
	if err != nil {
		m.cfg.logger.Error("secret resolution failed", "error", err)
		return nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(values)) {
		field(name)
		field(values[name].Reveal())
	}
	return h.Sum(nil), nil
}

// configType is the config type for a document the source reported as format.
func (m *Manager) configType(format string) string {
	if m.cfg.configType != "" {
		return m.cfg.configType
	}
	return format
}
