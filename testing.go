package lemonfig

import "fmt"

// TestVal creates a static [Val] that always returns value from [Val.Get].
// It is not reactive and has no real [Manager] behind it — intended for unit
// tests where a function or component accepts a *Val[T] but you don't need
// full config management.
//
// Derived values created via [Map], [MapWithCleanup], [Combine], and [Combine3]
// work as expected: they are computed eagerly when registered and their results
// are available immediately via Get.
//
//	cfg := lemonfig.TestVal(AppConfig{Port: 8080, Host: "localhost"})
//	port := lemonfig.Map(cfg, func(c AppConfig) (int, error) { return c.Port, nil })
//	port.Get() // 8080
func TestVal[T any](value T) *Val[T] {
	mgr := &Manager{eagerRecompute: true}
	id := mgr.nextID()
	gen := &generation{
		values: map[derivedID]any{id: value},
	}
	mgr.current.Store(gen)
	return &Val[T]{id: id, mgr: mgr}
}

// recomputeTestNodes walks all registered derived nodes and rebuilds the
// generation. Called by addNode when eagerRecompute is set.
func (m *Manager) recomputeTestNodes() {
	old := m.current.Load()
	b := &generationBuilder{
		values: make(map[derivedID]any),
		dirty:  make(map[derivedID]bool),
	}
	if old != nil {
		for k, v := range old.values {
			b.values[k] = v
			b.dirty[k] = true
		}
	}
	for _, node := range m.allNodes {
		val, changed, err := node.recompute(b, nil)
		if err != nil {
			panic(fmt.Sprintf("lemonfig.TestVal: transform error: %v", err))
		}
		b.set(node.nodeID(), val, changed)
	}
	m.current.Store(b.freeze(0))
}
