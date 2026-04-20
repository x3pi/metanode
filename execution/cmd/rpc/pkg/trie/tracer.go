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
