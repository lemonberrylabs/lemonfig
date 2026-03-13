package lemonfig

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"
)

// Manager is the central orchestrator for reactive configuration.
// It holds the current generation, owns a [ConfigSource], and coordinates
// atomic reloads of all derived values.
type Manager struct {
	current atomic.Pointer[generation]
	source  ConfigSource
	mu      sync.Mutex // serializes reloads

	// DAG of derived values, registered at startup.
	roots    []derivedNode // nodes that extract directly from Viper
	allNodes []derivedNode // topologically sorted, roots first

	// Config
	cfg    managerConfig
	nextID_ derivedID
	frozen  bool

	// Lifecycle
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewManager creates a [Manager] with the given source and options.
// It performs the initial config fetch and builds the first generation.
// Values ([Key], [Struct], [Map], [Combine]) should be registered
// after NewManager returns but before [Manager.Start] is called.
func NewManager(source ConfigSource, opts ...Option) (*Manager, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	m := &Manager{
		source: source,
		cfg:    cfg,
	}
	return m, nil
}

func (m *Manager) nextID() derivedID {
	id := m.nextID_
	m.nextID_++
	return id
}

func (m *Manager) mustNotBeFrozen() {
	if m.frozen {
		panic(ErrFrozen)
	}
}

func (m *Manager) addNode(node derivedNode, isRoot bool) {
	if isRoot {
		m.roots = append(m.roots, node)
	}
	m.allNodes = append(m.allNodes, node)
}

// Start freezes the DAG, performs the initial load, and begins watching
// the source for changes (if it implements [WatchableSource]).
// All [Key], [Struct], [Map], and [Combine] calls must happen before Start.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return ErrAlreadyStarted
	}
	m.frozen = true

	// Initial load.
	if err := m.reloadLocked(ctx); err != nil {
		return fmt.Errorf("lemonfig: initial load failed: %w", err)
	}

	m.started = true
	m.done = make(chan struct{})

	// If the source supports watching, start a background goroutine.
	if ws, ok := m.source.(WatchableSource); ok {
		watchCtx, cancel := context.WithCancel(ctx)
		m.cancel = cancel
		go func() {
			defer close(m.done)
			if err := ws.Watch(watchCtx, func() {
				if err := m.Reload(watchCtx); err != nil {
					m.cfg.logger.Error("reload failed", "error", err)
				}
			}); err != nil && watchCtx.Err() == nil {
				m.cfg.logger.Error("watch error", "error", err)
			}
		}()
	} else {
		close(m.done)
	}

	return nil
}

// Stop stops watching for config changes and runs final cleanup.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return ErrNotStarted
	}
	if m.cancel != nil {
		m.cancel()
	}
	<-m.done
	m.started = false
	return nil
}

// Reload manually triggers a config reload. It fetches new config,
// rebuilds the generation, and atomically swaps it in.
// If any step fails, the old generation is preserved.
func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloadLocked(ctx)
}

// OnReload registers a callback that fires after each successful reload
// with the old and new Viper instances.
func (m *Manager) OnReload(fn func(old, new_ *viper.Viper)) {
	m.cfg.onReload = append(m.cfg.onReload, fn)
}

func (m *Manager) reloadLocked(ctx context.Context) error {
	data, format, err := m.source.Fetch(ctx)
	if err != nil {
		m.cfg.logger.Error("fetch failed", "error", err)
		return fmt.Errorf("%w: %w", ErrFetchFailed, err)
	}

	// Determine config type.
	cfgType := m.cfg.configType
	if cfgType == "" {
		cfgType = format
	}

	v := viper.New()
	v.SetConfigType(cfgType)
	if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
		m.cfg.logger.Error("parse failed", "error", err)
		return fmt.Errorf("%w: %w", ErrParseFailed, err)
	}

	// Validate if configured.
	if m.cfg.validate != nil {
		if err := m.cfg.validate(v); err != nil {
			m.cfg.logger.Error("validation failed", "error", err)
			return fmt.Errorf("%w: %w", ErrValidationFailed, err)
		}
	}

	oldGen := m.current.Load()
	builder := newGenerationBuilder(v)

	// Walk DAG in topological order (roots first).
	for _, node := range m.allNodes {
		val, changed, err := node.recompute(builder, oldGen)
		if err != nil {
			m.cfg.logger.Error("transform failed",
				"node", node.nodeID(),
				"error", err,
			)
			return fmt.Errorf("%w: node %d: %w", ErrTransformFailed, node.nodeID(), err)
		}
		builder.set(node.nodeID(), val, changed)
	}

	// Compute new version.
	var version int64
	if oldGen != nil {
		version = oldGen.version + 1
	}
	newGen := builder.freeze(version)
	m.current.Store(newGen)

	// Fire OnReload callbacks.
	if oldGen != nil {
		for _, fn := range m.cfg.onReload {
			fn(oldGen.config, newGen.config)
		}
	}

	m.cfg.logger.Info("config reloaded", "version", version)

	// Schedule cleanup of old generation resources.
	if oldGen != nil {
		m.scheduleCleanup(oldGen, builder)
	}

	return nil
}

func (m *Manager) scheduleCleanup(oldGen *generation, builder *generationBuilder) {
	// Collect nodes that changed and have cleanup.
	type cleanupItem struct {
		node derivedNode
		old  any
	}
	var items []cleanupItem
	// Walk in reverse topological order (leaves first).
	for i := len(m.allNodes) - 1; i >= 0; i-- {
		node := m.allNodes[i]
		nid := node.nodeID()
		if builder.dirty[nid] {
			if oldVal, ok := oldGen.values[nid]; ok {
				items = append(items, cleanupItem{node: node, old: oldVal})
			}
		}
	}
	if len(items) == 0 {
		return
	}

	go func() {
		time.Sleep(m.cfg.cleanupGrace)
		for _, item := range items {
			func() {
				defer func() {
					if r := recover(); r != nil {
						m.cfg.logger.Error("cleanup panic", "node", item.node.nodeID(), "panic", r)
					}
				}()
				item.node.cleanupOld(item.old)
			}()
		}
	}()
}
