package lemonfig

import (
	"reflect"

	"github.com/spf13/viper"
)

// derivedNode is the type-erased internal representation of a DAG node.
type derivedNode interface {
	nodeID() derivedID
	parents() []derivedID
	// recompute produces the value for the new generation.
	// Returns the value, whether it changed vs the old generation, and any error.
	recompute(b *generationBuilder, old *generation) (any, bool, error)
	// cleanupOld is called on the OLD value after swap. May be nil.
	cleanupOld(old any)
}

// Derived represents a reactive value of type T derived from config.
// Call [Derived.Get] to obtain the current value, which is always consistent
// with the latest successful reload.
//
// The primary way to create a Derived is [Load], which unmarshals the entire
// config into a struct. Sub-fields are then extracted using the package-level
// [Map] function:
//
//	mgr, _ := lemonfig.NewManager(source)
//	cfg := lemonfig.Load[Config](mgr)
//	env := lemonfig.Map(cfg, func(c Config) (Environment, error) { return c.Environment, nil })
//	mgr.Start(ctx)
//
// Note: [Map], [MapWithCleanup], [Combine], and [Combine3] are package-level
// functions because Go does not support methods with additional type parameters.
type Derived[T any] struct {
	id  derivedID
	mgr *Manager
}

// Get returns the current value of this derived node.
// It performs a single atomic pointer load followed by a map lookup — lock-free
// and safe to call from any goroutine.
func (d *Derived[T]) Get() T {
	gen := d.mgr.current.Load()
	v, ok := gen.values[d.id]
	if !ok {
		var zero T
		return zero
	}
	return v.(T)
}

// Load creates a [Derived] that unmarshals the entire config into a struct
// of type T. This is the primary entry point — use [Map] to extract sub-fields
// or derive resources from the loaded config.
//
// T should use `mapstructure` struct tags for field mapping.
//
// Must be called before [Manager.Start].
//
//	mgr, _ := lemonfig.NewManager(source)
//	cfg := lemonfig.Load[Config](mgr)
//	port := lemonfig.Map(cfg, func(c Config) (int, error) { return c.Server.Port, nil })
//	mgr.Start(ctx)
func Load[T any](mgr *Manager) *Derived[T] {
	return Struct[T](mgr, "")
}

// --- Root nodes: Key and Struct ---

type keyNode[T any] struct {
	id_  derivedID
	path string
}

func (n *keyNode[T]) nodeID() derivedID    { return n.id_ }
func (n *keyNode[T]) parents() []derivedID { return nil }
func (n *keyNode[T]) cleanupOld(any)       {}

func (n *keyNode[T]) recompute(b *generationBuilder, old *generation) (any, bool, error) {
	val := viperGet[T](b.config, n.path)
	changed := true
	if old != nil {
		if oldVal, ok := old.values[n.id_]; ok {
			changed = !reflect.DeepEqual(val, oldVal)
		}
	}
	return val, changed, nil
}

// viperGet extracts a value of type T from a Viper instance at the given path.
func viperGet[T any](v *viper.Viper, path string) T {
	var zero T
	raw := v.Get(path)
	if raw == nil {
		return zero
	}
	if typed, ok := raw.(T); ok {
		return typed
	}
	// For numeric types and other conversions, let Viper handle it via its typed getters.
	switch any(zero).(type) {
	case string:
		return any(v.GetString(path)).(T)
	case int:
		return any(v.GetInt(path)).(T)
	case int32:
		return any(v.GetInt32(path)).(T)
	case int64:
		return any(v.GetInt64(path)).(T)
	case float64:
		return any(v.GetFloat64(path)).(T)
	case bool:
		return any(v.GetBool(path)).(T)
	case []string:
		return any(v.GetStringSlice(path)).(T)
	case map[string]any:
		return any(v.GetStringMap(path)).(T)
	case map[string]string:
		return any(v.GetStringMapString(path)).(T)
	}
	return zero
}

// Key creates a [Derived] that extracts a single config value by Viper path.
// For most use cases, prefer [Load] with [Map] instead.
//
// Must be called before [Manager.Start].
func Key[T any](mgr *Manager, path string) *Derived[T] {
	mgr.mustNotBeFrozen()
	id := mgr.nextID()
	node := &keyNode[T]{id_: id, path: path}
	mgr.addNode(node, true)
	return &Derived[T]{id: id, mgr: mgr}
}

type structNode[T any] struct {
	id_  derivedID
	path string
}

func (n *structNode[T]) nodeID() derivedID    { return n.id_ }
func (n *structNode[T]) parents() []derivedID { return nil }
func (n *structNode[T]) cleanupOld(any)       {}

func (n *structNode[T]) recompute(b *generationBuilder, old *generation) (any, bool, error) {
	var val T
	sub := b.config
	if n.path != "" {
		sub = b.config.Sub(n.path)
		if sub == nil {
			// Path doesn't exist; return zero value.
			changed := true
			if old != nil {
				if oldVal, ok := old.values[n.id_]; ok {
					changed = !reflect.DeepEqual(val, oldVal)
				}
			}
			return val, changed, nil
		}
	}
	if err := sub.Unmarshal(&val); err != nil {
		return val, false, err
	}
	changed := true
	if old != nil {
		if oldVal, ok := old.values[n.id_]; ok {
			changed = !reflect.DeepEqual(val, oldVal)
		}
	}
	return val, changed, nil
}

// Struct creates a [Derived] that unmarshals a config sub-tree into a struct of type T.
// The path may be empty to unmarshal the entire config. For loading the full config,
// prefer [Load] which wraps this with manager creation.
//
// Must be called before [Manager.Start].
func Struct[T any](mgr *Manager, path string) *Derived[T] {
	mgr.mustNotBeFrozen()
	id := mgr.nextID()
	node := &structNode[T]{id_: id, path: path}
	mgr.addNode(node, true)
	return &Derived[T]{id: id, mgr: mgr}
}

// --- Transform nodes: Map, MapWithCleanup ---

type mapNode[T, R any] struct {
	id_       derivedID
	parentID  derivedID
	transform func(T) (R, error)
	cleanup   func(R)
}

func (n *mapNode[T, R]) nodeID() derivedID    { return n.id_ }
func (n *mapNode[T, R]) parents() []derivedID { return []derivedID{n.parentID} }

func (n *mapNode[T, R]) cleanupOld(old any) {
	if n.cleanup != nil {
		n.cleanup(old.(R))
	}
}

func (n *mapNode[T, R]) recompute(b *generationBuilder, old *generation) (any, bool, error) {
	// If parent didn't change, carry forward old value.
	if !b.dirty[n.parentID] && old != nil {
		if oldVal, ok := old.values[n.id_]; ok {
			return oldVal, false, nil
		}
	}
	parentVal := b.values[n.parentID].(T)
	result, err := n.transform(parentVal)
	if err != nil {
		return result, false, err
	}
	changed := true
	if old != nil {
		if oldVal, ok := old.values[n.id_]; ok {
			changed = !reflect.DeepEqual(result, oldVal)
		}
	}
	return result, changed, nil
}

// Map transforms a [Derived][T] into a [Derived][R].
// The transform function is called during reload to produce the new R value.
// If the parent value hasn't changed, the transform is not called.
//
// This is a package-level function (not a method) because Go does not support
// methods with additional type parameters.
//
// Must be called before [Derived.Start].
func Map[T, R any](parent *Derived[T], fn func(T) (R, error)) *Derived[R] {
	parent.mgr.mustNotBeFrozen()
	id := parent.mgr.nextID()
	node := &mapNode[T, R]{id_: id, parentID: parent.id, transform: fn}
	parent.mgr.addNode(node, false)
	return &Derived[R]{id: id, mgr: parent.mgr}
}

// MapWithCleanup is like [Map] but includes a cleanup function for the old value.
// The cleanup function is called after the generation swap with a grace period,
// in reverse topological order.
//
// Must be called before [Derived.Start].
func MapWithCleanup[T, R any](parent *Derived[T], fn func(T) (R, error), cleanup func(R)) *Derived[R] {
	parent.mgr.mustNotBeFrozen()
	id := parent.mgr.nextID()
	node := &mapNode[T, R]{id_: id, parentID: parent.id, transform: fn, cleanup: cleanup}
	parent.mgr.addNode(node, false)
	return &Derived[R]{id: id, mgr: parent.mgr}
}

// --- Combine nodes ---

type combineNode[T, U, R any] struct {
	id_      derivedID
	parentA  derivedID
	parentB  derivedID
	combine  func(T, U) (R, error)
}

func (n *combineNode[T, U, R]) nodeID() derivedID    { return n.id_ }
func (n *combineNode[T, U, R]) parents() []derivedID { return []derivedID{n.parentA, n.parentB} }
func (n *combineNode[T, U, R]) cleanupOld(any)       {}

func (n *combineNode[T, U, R]) recompute(b *generationBuilder, old *generation) (any, bool, error) {
	if !b.dirty[n.parentA] && !b.dirty[n.parentB] && old != nil {
		if oldVal, ok := old.values[n.id_]; ok {
			return oldVal, false, nil
		}
	}
	aVal := b.values[n.parentA].(T)
	bVal := b.values[n.parentB].(U)
	result, err := n.combine(aVal, bVal)
	if err != nil {
		return result, false, err
	}
	changed := true
	if old != nil {
		if oldVal, ok := old.values[n.id_]; ok {
			changed = !reflect.DeepEqual(result, oldVal)
		}
	}
	return result, changed, nil
}

// Combine takes two [Derived] values and produces a new [Derived] from both.
// The combine function is re-evaluated when either parent changes.
//
// Must be called before [Derived.Start].
func Combine[T, U, R any](a *Derived[T], b *Derived[U], fn func(T, U) (R, error)) *Derived[R] {
	a.mgr.mustNotBeFrozen()
	id := a.mgr.nextID()
	node := &combineNode[T, U, R]{id_: id, parentA: a.id, parentB: b.id, combine: fn}
	a.mgr.addNode(node, false)
	return &Derived[R]{id: id, mgr: a.mgr}
}

type combine3Node[T, U, V, R any] struct {
	id_      derivedID
	parentA  derivedID
	parentB  derivedID
	parentC  derivedID
	combine  func(T, U, V) (R, error)
}

func (n *combine3Node[T, U, V, R]) nodeID() derivedID { return n.id_ }
func (n *combine3Node[T, U, V, R]) parents() []derivedID {
	return []derivedID{n.parentA, n.parentB, n.parentC}
}
func (n *combine3Node[T, U, V, R]) cleanupOld(any) {}

func (n *combine3Node[T, U, V, R]) recompute(b *generationBuilder, old *generation) (any, bool, error) {
	if !b.dirty[n.parentA] && !b.dirty[n.parentB] && !b.dirty[n.parentC] && old != nil {
		if oldVal, ok := old.values[n.id_]; ok {
			return oldVal, false, nil
		}
	}
	aVal := b.values[n.parentA].(T)
	bVal := b.values[n.parentB].(U)
	cVal := b.values[n.parentC].(V)
	result, err := n.combine(aVal, bVal, cVal)
	if err != nil {
		return result, false, err
	}
	changed := true
	if old != nil {
		if oldVal, ok := old.values[n.id_]; ok {
			changed = !reflect.DeepEqual(result, oldVal)
		}
	}
	return result, changed, nil
}

// Combine3 takes three [Derived] values and produces a new [Derived] from all three.
// The combine function is re-evaluated when any parent changes.
//
// Must be called before [Derived.Start].
func Combine3[T, U, V, R any](a *Derived[T], b *Derived[U], c *Derived[V], fn func(T, U, V) (R, error)) *Derived[R] {
	a.mgr.mustNotBeFrozen()
	id := a.mgr.nextID()
	node := &combine3Node[T, U, V, R]{
		id_: id, parentA: a.id, parentB: b.id, parentC: c.id, combine: fn,
	}
	a.mgr.addNode(node, false)
	return &Derived[R]{id: id, mgr: a.mgr}
}
