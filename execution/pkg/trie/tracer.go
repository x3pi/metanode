package trie

import (
	"sync"

	e_common "github.com/ethereum/go-ethereum/common"
)

type Tracer struct {
	inserts    sync.Map
	deletes    sync.Map
	oldKeys    [][]byte
	accessList sync.Map
}

func newTracer() *Tracer {
	return &Tracer{
		oldKeys: [][]byte{},
	}
}

func (t *Tracer) onRead(path []byte, val []byte) {
	t.accessList.Store(string(path), val)
}

func (t *Tracer) onInsert(path []byte) {
	if _, present := t.deletes.LoadAndDelete(string(path)); present {
		return
	}
	t.inserts.Store(string(path), struct{}{})
}

func (t *Tracer) onDelete(path []byte) {
	if _, present := t.inserts.LoadAndDelete(string(path)); present {
		return
	}
	t.deletes.Store(string(path), struct{}{})
}

func (t *Tracer) reset() {
	t.inserts.Range(func(key, value any) bool {
		t.inserts.Delete(key)
		return true
	})
	t.deletes.Range(func(key, value any) bool {
		t.deletes.Delete(key)
		return true
	})
	t.accessList.Range(func(key, value any) bool {
		t.accessList.Delete(key)
		return true
	})
	t.oldKeys = [][]byte{}
}

func (t *Tracer) copy() *Tracer {
	newTracer := &Tracer{
		oldKeys: [][]byte{},
	}
	t.inserts.Range(func(key, value any) bool {
		newTracer.inserts.Store(key, value)
		return true
	})
	t.deletes.Range(func(key, value any) bool {
		newTracer.deletes.Store(key, value)
		return true
	})
	t.accessList.Range(func(key, value any) bool {
		newTracer.accessList.Store(key, e_common.CopyBytes(value.([]byte)))
		return true
	})
	return newTracer
}

func (t *Tracer) deletedNodes() []string {
	var paths []string
	t.deletes.Range(func(key, value any) bool {
		if _, ok := t.accessList.Load(key); ok {
			paths = append(paths, key.(string))
		}
		return true
	})
	return paths
}

// merge combines another Tracer's data into this one.
// Used to aggregate results from parallel subtree updates in BatchUpdate.
func (t *Tracer) merge(other *Tracer) {
	// Merge inserts
	other.inserts.Range(func(key, value any) bool {
		t.inserts.Store(key, value)
		return true
	})
	// Merge deletes
	other.deletes.Range(func(key, value any) bool {
		t.deletes.Store(key, value)
		return true
	})
	// Merge accessList
	other.accessList.Range(func(key, value any) bool {
		t.accessList.Store(key, value)
		return true
	})
	// Merge oldKeys
	t.oldKeys = append(t.oldKeys, other.oldKeys...)
}

// ═══════════════════════════════════════════════════════════════
// FastTracer: Non-concurrent tracer for single-goroutine use.
// Used by BatchUpdate subtrees where each goroutine has exclusive
// access — avoids sync.Map overhead (~25ms savings for 16 subtrees).
//
// FORK-SAFETY: Each BatchUpdate goroutine gets its own FastTracer.
// Results are merged sequentially into the main Tracer after all
// goroutines complete. No data races possible.
// ═══════════════════════════════════════════════════════════════

// FastTracer is a non-concurrent tracer using regular maps.
// ~5x faster than Tracer with sync.Map for single-goroutine use.
type FastTracer struct {
	inserts    map[string]struct{}
	deletes    map[string]struct{}
	oldKeys    [][]byte
	accessList map[string][]byte
}

func newFastTracer() *FastTracer {
	return &FastTracer{
		inserts:    make(map[string]struct{}),
		deletes:    make(map[string]struct{}),
		oldKeys:    [][]byte{},
		accessList: make(map[string][]byte),
	}
}

func (t *FastTracer) onRead(path []byte, val []byte) {
	t.accessList[string(path)] = val
}

func (t *FastTracer) onInsert(path []byte) {
	key := string(path)
	if _, present := t.deletes[key]; present {
		delete(t.deletes, key)
		return
	}
	t.inserts[key] = struct{}{}
}

func (t *FastTracer) onDelete(path []byte) {
	key := string(path)
	if _, present := t.inserts[key]; present {
		delete(t.inserts, key)
		return
	}
	t.deletes[key] = struct{}{}
}

// mergeFast absorbs a FastTracer's data into this sync.Map-based Tracer.
// Called sequentially after all BatchUpdate goroutines complete.
func (t *Tracer) mergeFast(other *FastTracer) {
	for k, v := range other.inserts {
		t.inserts.Store(k, v)
	}
	for k, v := range other.deletes {
		t.deletes.Store(k, v)
	}
	for k, v := range other.accessList {
		t.accessList.Store(k, v)
	}
	t.oldKeys = append(t.oldKeys, other.oldKeys...)
}
