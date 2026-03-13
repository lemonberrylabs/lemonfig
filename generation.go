package lemonfig

import "github.com/spf13/viper"

// derivedID uniquely identifies a derived node in the DAG.
type derivedID int

// generation is an immutable snapshot of all derived values.
// It is atomically swapped on successful reload.
type generation struct {
	version int64
	config  *viper.Viper
	values  map[derivedID]any
}

// generationBuilder constructs the next generation during a reload.
type generationBuilder struct {
	config *viper.Viper
	values map[derivedID]any
	dirty  map[derivedID]bool
}

func newGenerationBuilder(v *viper.Viper) *generationBuilder {
	return &generationBuilder{
		config: v,
		values: make(map[derivedID]any),
		dirty:  make(map[derivedID]bool),
	}
}

func (b *generationBuilder) set(id derivedID, val any, changed bool) {
	b.values[id] = val
	b.dirty[id] = changed
}

func (b *generationBuilder) freeze(version int64) *generation {
	return &generation{
		version: version,
		config:  b.config,
		values:  b.values,
	}
}
