package lemonfig

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

const secretTag = "!secret"

// SecretResolver returns the value of the secret called name, the scalar that
// follows a !secret tag in a YAML document.
type SecretResolver func(ctx context.Context, name string) (string, error)

// SecretTagError describes one !secret tag that cannot be accepted.
type SecretTagError struct {
	Path   string // where the tagged value lands, e.g. "providers[1].api_key"
	Name   string // the secret name that follows the tag
	Reason string
}

func (e SecretTagError) Error() string {
	return fmt.Sprintf("%s: !secret %s: %s", e.Path, e.Name, e.Reason)
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

// secretUse is one place in the decoded document where a tagged scalar lands.
// A scalar under a YAML anchor lands once per alias or merge that reaches it.
type secretUse struct {
	path []pathSeg
	ref  int // index into taggedDoc.names
}

// taggedDoc is a decoded YAML document in which every !secret scalar has been
// replaced by a placeholder string.
//
// The document is decoded by yaml.v3 itself and the placeholders are then
// located in the decoded tree, so a path is where the value really lands after
// aliases and merge keys are applied, not where the tag was written. The tree
// is map[string]any because that is what yaml.v3 produces and what Viper
// consumes.
type taggedDoc struct {
	tree   map[string]any
	prefix string   // placeholder prefix, random per document
	names  []string // secret name per tagged scalar
	uses   []secretUse
}

func parseTagged(data []byte) (*taggedDoc, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	d := &taggedDoc{prefix: "lemonfig-secret-" + rand.Text() + "-"}
	if root.Kind == 0 { // empty document
		return d, nil
	}
	if err := d.mark(&root); err != nil {
		return nil, err
	}
	if err := root.Decode(&d.tree); err != nil {
		return nil, err
	}
	err := d.walk(d.tree, nil, func(path []pathSeg, ref int) (Secret, bool) {
		d.uses = append(d.uses, secretUse{path: slices.Clone(path), ref: ref})
		return Secret{}, false
	})
	return d, err
}

// mark replaces every !secret scalar under n with a placeholder.
func (d *taggedDoc) mark(n *yaml.Node) error {
	if n.Tag == secretTag {
		if n.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: !secret must tag a scalar", n.Line)
		}
		if n.Value == "" {
			return fmt.Errorf("line %d: !secret needs a secret name", n.Line)
		}
		n.Tag = "!!str"
		n.Value, d.names = d.prefix+strconv.Itoa(len(d.names)), append(d.names, n.Value)
		return nil
	}
	for _, c := range n.Content {
		if err := d.mark(c); err != nil {
			return err
		}
	}
	return nil
}

// ref reports which tagged scalar the string s stands for, if any.
func (d *taggedDoc) ref(s string) (int, bool) {
	rest, ok := strings.CutPrefix(s, d.prefix)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	return i, err == nil && i >= 0 && i < len(d.names)
}

// walk visits every placeholder in v in a stable order. When visit returns
// true the placeholder is replaced by the returned Secret.
func (d *taggedDoc) walk(v any, path []pathSeg, visit func([]pathSeg, int) (Secret, bool)) error {
	// child handles one element and reports its replacement, if any.
	child := func(seg pathSeg, c any) (Secret, bool, error) {
		p := append(path, seg)
		if s, ok := c.(string); ok {
			if ref, ok := d.ref(s); ok {
				sec, replace := visit(p, ref)
				return sec, replace, nil
			}
		}
		return Secret{}, false, d.walk(c, p, visit)
	}
	taggedKey := func(k string) error {
		if ref, ok := d.ref(k); ok {
			return fmt.Errorf("%s: !secret %s tags a mapping key", formatPath(path), d.names[ref])
		}
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(t)) {
			if err := taggedKey(k); err != nil {
				return err
			}
			sec, replace, err := child(pathSeg{key: k}, t[k])
			if err != nil {
				return err
			}
			if replace {
				t[k] = sec
			}
		}
	case map[any]any: // yaml.v3 produces this for a mapping with a non-string key
		keys := make(map[string]any, len(t))
		for k := range t {
			keys[fmt.Sprint(k)] = k
		}
		for _, name := range slices.Sorted(maps.Keys(keys)) {
			if err := taggedKey(name); err != nil {
				return err
			}
			sec, replace, err := child(pathSeg{key: name}, t[keys[name]])
			if err != nil {
				return err
			}
			if replace {
				t[keys[name]] = sec
			}
		}
	case []any:
		for i, c := range t {
			sec, replace, err := child(pathSeg{index: i, isIndex: true}, c)
			if err != nil {
				return err
			}
			if replace {
				t[i] = sec
			}
		}
	}
	return nil
}

// fill replaces every placeholder with the Secret for its name.
func (d *taggedDoc) fill(values map[string]Secret) {
	// walk only fails on a tagged key, which parseTagged already rejected.
	_ = d.walk(d.tree, nil, func(_ []pathSeg, ref int) (Secret, bool) {
		return values[d.names[ref]], true
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

// violations returns one error for every tagged scalar that does not land on
// a [Secret] in every target that reads its path, or that no target reads.
func (d *taggedDoc) violations(targets []secretTarget) []SecretTagError {
	var out []SecretTagError
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
			out = append(out, SecretTagError{Path: formatPath(use.path), Name: d.names[use.ref], Reason: reason})
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

// CheckSecretTags reports every !secret tag in a YAML document that does not
// land on a [Secret]-typed field of T, following `mapstructure` tags the way
// [Load] decodes. It resolves nothing and needs no [Manager]; use it to
// reject a document before it is stored.
//
// A tag is accepted on a Secret field at any depth, including inside maps and
// slices of structs, and wherever a YAML alias or merge key carries it. It is
// reported on any other type (string, any, a key T does not declare).
//
// The error is non-nil when the document is not valid YAML, or when a !secret
// tag sits on a mapping key or a non-scalar, or has no name.
func CheckSecretTags[T any](doc []byte) ([]SecretTagError, error) {
	d, err := parseTagged(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseFailed, err)
	}
	return d.violations([]secretTarget{{typ: reflect.TypeFor[T]()}}), nil
}

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

// resolveSecrets calls the resolver once per distinct secret name in d.
func (m *Manager) resolveSecrets(ctx context.Context, d *taggedDoc) (map[string]Secret, error) {
	values := make(map[string]Secret)
	for _, use := range d.uses {
		name := d.names[use.ref]
		if _, ok := values[name]; ok {
			continue
		}
		v, err := m.cfg.secretResolver(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("%w: %s (at %s): %w", ErrSecretResolveFailed, name, formatPath(use.path), err)
		}
		sec := NewSecret(v)
		sec.ref = name
		values[name] = sec
	}
	return values, nil
}

// readSecretYAML reads a YAML document into v with every !secret scalar
// resolved to a [Secret] value. Nothing is resolved unless every tag lands on
// a Secret field.
func (m *Manager) readSecretYAML(ctx context.Context, v *viper.Viper, data []byte) error {
	d, err := parseTagged(data)
	if err != nil {
		m.cfg.logger.Error("parse failed", "error", err)
		return fmt.Errorf("%w: %w", ErrParseFailed, err)
	}
	if bad := d.violations(m.secretTargets()); len(bad) > 0 {
		errs := make([]error, len(bad))
		for i, e := range bad {
			errs[i] = e
		}
		err := fmt.Errorf("%w: %w", ErrSecretTag, errors.Join(errs...))
		m.cfg.logger.Error("secret tag rejected", "error", err)
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

// secretChangeKey is the [ChangeKeyer] function a Manager with a secret
// resolver installs: a digest of the document and of every secret it
// references, so a rotated secret reads as a change.
//
// A document the reload would reject (unparsable, or a tag on a non-Secret
// path) is keyed on its bytes alone and nothing is resolved: the reload then
// runs and reports the rejection.
func (m *Manager) secretChangeKey(ctx context.Context, data []byte) ([]byte, error) {
	h := sha256.New()
	field := func(s string) {
		h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(s))))
		h.Write([]byte(s))
	}
	field(string(data))
	d, err := parseTagged(data)
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
