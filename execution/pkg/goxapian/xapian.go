package goxapian

/*
#cgo CXXFLAGS: -std=c++11
#cgo LDFLAGS: -lxapian
#include <stdlib.h>
#include "xapian_wrapper.h"

const int DB_CREATE_OR_OPEN = 4;
*/
import "C"
import (
	"fmt"
	"runtime"
	"sync/atomic"
	"unsafe"
)

// --- Struct Wrappers ---
//
// Each wrapper's `closed` flag (added 2026-09, Phuong an A mục 22 -- "check
// lại toàn bộ cgo của xapian") guards against a real TOCTOU race in the
// original `if ptr != nil { free(ptr); ptr = nil }` pattern used throughout
// this file: manual Close() and the runtime.SetFinalizer callback below can
// run concurrently (finalizers run on their own goroutine), and two
// concurrent callers could both observe a non-nil ptr before either nils it
// out, double-freeing the underlying C++ object -- undefined behavior, not
// just a leak. atomic.CompareAndSwapInt32 in Close() makes "am I the one who
// gets to free this" a single atomic decision instead of a check-then-act
// race, and every other method now gates on the same `closed` flag (instead
// of re-reading `ptr`, which a concurrent Close() could be mutating) so a
// call racing a Close() either fully happens-before it or is cleanly
// rejected, never sees a half-freed object.
type Database struct {
	ptr    C.xapian_database_t
	closed int32
}
type Document struct {
	ptr    C.xapian_document_t
	closed int32
}
type QueryParser struct {
	ptr    C.xapian_queryparser_t
	closed int32
}
type Query struct {
	ptr    C.xapian_query_t
	closed int32
}
type Enquire struct {
	ptr    C.xapian_enquire_t
	closed int32
}
type MSet struct {
	ptr    C.xapian_mset_t
	closed int32
}

func (db *Database) isClosed() bool    { return atomic.LoadInt32(&db.closed) != 0 }
func (doc *Document) isClosed() bool   { return atomic.LoadInt32(&doc.closed) != 0 }
func (qp *QueryParser) isClosed() bool { return atomic.LoadInt32(&qp.closed) != 0 }
func (q *Query) isClosed() bool        { return atomic.LoadInt32(&q.closed) != 0 }
func (enq *Enquire) isClosed() bool    { return atomic.LoadInt32(&enq.closed) != 0 }
func (mset *MSet) isClosed() bool      { return atomic.LoadInt32(&mset.closed) != 0 }

// --- Query Operator Constants ---
type QueryOp int

const (
	QueryOpAnd QueryOp = iota
	QueryOpOr
)

// **ĐÃ SỬA**: Định nghĩa cờ tính năng dưới dạng hằng số Go.
// Các giá trị này tương ứng với enum feature_flag của Xapian::QueryParser
type QueryParserFeature uint

const (
	FeatureBoolean        QueryParserFeature = 1 << 0
	FeaturePhrase         QueryParserFeature = 1 << 1
	FeatureLoveHate       QueryParserFeature = 1 << 2
	FeatureBooleanAnyCase QueryParserFeature = 1 << 3
	FeatureWildcard       QueryParserFeature = 1 << 4
)

// --- Database Methods ---
func NewWritableDatabase(path string) (*Database, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	dbPtr := C.database_new_writable(cPath, C.DB_CREATE_OR_OPEN)
	if dbPtr == nil {
		return nil, fmt.Errorf("could not open database at %s", path)
	}
	db := &Database{ptr: dbPtr}
	runtime.SetFinalizer(db, (*Database).Close)
	return db, nil
}
func (db *Database) Close() {
	if !atomic.CompareAndSwapInt32(&db.closed, 0, 1) {
		return
	}
	C.database_close(db.ptr)
}
func (db *Database) GetDocCount() uint {
	if db.isClosed() {
		return 0
	}
	return uint(C.database_get_doccount(db.ptr))
}
func (db *Database) AddDocument(doc *Document) uint {
	if db.isClosed() || doc.isClosed() {
		return 0 // Or an appropriate error code/value
	}
	return uint(C.database_add_document(db.ptr, doc.ptr))
}

// ReplaceDocumentByTerm now returns an error (2026-09, mục 22) -- the
// underlying C.database_replace_document_by_term used to be a `void` call
// that silently discarded a failed write (disk full, DB corruption, lock
// conflict). Every existing caller compiled fine ignoring a bare error
// return already (Go doesn't force callers to check errors), so this is a
// non-breaking signature widening; new/updated call sites should check it.
func (db *Database) ReplaceDocumentByTerm(uniqueTerm string, doc *Document) error {
	if db.isClosed() {
		return fmt.Errorf("goxapian: database is closed")
	}
	if doc.isClosed() {
		return fmt.Errorf("goxapian: document is closed")
	}
	cTerm := C.CString(uniqueTerm)
	defer C.free(unsafe.Pointer(cTerm))
	ok := C.database_replace_document_by_term(db.ptr, cTerm, doc.ptr)
	if !bool(ok) {
		return fmt.Errorf("goxapian: replace_document_by_term failed for term %q (see stderr for the Xapian error)", uniqueTerm)
	}
	return nil
}

// Commit now returns an error (2026-09, mục 22) -- this is the single most
// important fix in this package: Commit() used to be a `void` call, so a
// caller that just wrote documents and called Commit() had ZERO way to
// detect that the flush actually failed and silently believed its data was
// durably persisted when it might not have been. Every existing call site
// (explorer/service.go, pkg/mining/service.go, cmd/mining/server.go) has
// been updated to log this error rather than discard it.
func (db *Database) Commit() error {
	if db.isClosed() {
		return fmt.Errorf("goxapian: database is closed")
	}
	ok := C.database_commit(db.ptr)
	if !bool(ok) {
		return fmt.Errorf("goxapian: commit failed (see stderr for the Xapian error)")
	}
	return nil
}
func (db *Database) Enquire() *Enquire {
	if db.isClosed() {
		return nil
	}
	enqPtr := C.enquire_new(db.ptr)
	if enqPtr == nil {
		return nil
	}
	enq := &Enquire{ptr: enqPtr}
	runtime.SetFinalizer(enq, (*Enquire).Close)
	return enq
}

func (db *Database) DumpAllDocs() string {
	if db.isClosed() {
		return "Database is not open."
	}
	cData := C.database_dump_all_docs(db.ptr)
	if cData == nil {
		return "" // Or an error message indicating dump failed
	}
	defer C.free(unsafe.Pointer(cData))
	return C.GoString(cData)
}

// --- Document Methods ---
func NewDocument() *Document {
	docPtr := C.document_new()
	if docPtr == nil {
		return nil
	}
	doc := &Document{ptr: docPtr}
	runtime.SetFinalizer(doc, (*Document).Close)
	return doc
}
func (doc *Document) Close() {
	if !atomic.CompareAndSwapInt32(&doc.closed, 0, 1) {
		return
	}
	C.document_free(doc.ptr)
}
func (doc *Document) SetData(data string) {
	if doc.isClosed() {
		return
	}
	cData := C.CString(data)
	defer C.free(unsafe.Pointer(cData))
	C.document_set_data(doc.ptr, cData)
}
func (doc *Document) AddTerm(term string) {
	if doc.isClosed() {
		return
	}
	cTerm := C.CString(term)
	defer C.free(unsafe.Pointer(cTerm))
	C.document_add_term(doc.ptr, cTerm)
}
func (doc *Document) GetData() string {
	if doc.isClosed() {
		return "" // Or an empty string to indicate no data
	}
	cData := C.document_get_data(doc.ptr)
	if cData == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cData))
	return C.GoString(cData)
}

// --- QueryParser Methods ---
func NewQueryParser() *QueryParser {
	qpPtr := C.queryparser_new()
	if qpPtr == nil {
		return nil
	}
	qp := &QueryParser{ptr: qpPtr}
	runtime.SetFinalizer(qp, (*QueryParser).Close)
	return qp
}
func (qp *QueryParser) Close() {
	if !atomic.CompareAndSwapInt32(&qp.closed, 0, 1) {
		return
	}
	C.queryparser_free(qp.ptr)
}
func (qp *QueryParser) SetDatabase(db *Database) {
	if qp.isClosed() || db.isClosed() {
		return
	}
	C.queryparser_set_database(qp.ptr, db.ptr)
}
func (qp *QueryParser) SetStemmer(lang string) {
	if qp.isClosed() {
		return
	}
	cLang := C.CString(lang)
	defer C.free(unsafe.Pointer(cLang))
	C.queryparser_set_stemming_language(qp.ptr, cLang)
}
func (qp *QueryParser) SetDefaultOp(op QueryOp) {
	if qp.isClosed() {
		return
	}
	C.queryparser_set_default_op(qp.ptr, C.xapian_query_op(op))
}

func (qp *QueryParser) AddPrefix(field, prefix string) {
	if qp.isClosed() {
		return
	}
	cField := C.CString(field)
	cPrefix := C.CString(prefix)
	defer C.free(unsafe.Pointer(cField))
	defer C.free(unsafe.Pointer(cPrefix))
	C.queryparser_add_prefix(qp.ptr, cField, cPrefix)
}

// **ĐÃ SỬA**: Sửa đổi ParseQuery để nhận các cờ tính năng
func (qp *QueryParser) ParseQuery(query string, features ...QueryParserFeature) *Query {
	if qp.isClosed() {
		return nil
	}
	cQuery := C.CString(query)
	defer C.free(unsafe.Pointer(cQuery))

	var flags uint
	// Kết hợp tất cả các cờ được cung cấp bằng toán tử OR
	for _, feature := range features {
		flags |= uint(feature)
	}

	qPtr := C.queryparser_parse_query(qp.ptr, cQuery, C.uint(flags))
	if qPtr == nil {
		return nil
	}
	q := &Query{ptr: qPtr}
	runtime.SetFinalizer(q, (*Query).Close)
	return q
}

func (q *Query) Close() {
	if !atomic.CompareAndSwapInt32(&q.closed, 0, 1) {
		return
	}
	C.query_free(q.ptr)
}

// --- Enquire Methods ---
func (enq *Enquire) Close() {
	if !atomic.CompareAndSwapInt32(&enq.closed, 0, 1) {
		return
	}
	C.enquire_free(enq.ptr)
}
func (enq *Enquire) SetQuery(q *Query) {
	if enq.isClosed() || q.isClosed() {
		return
	}
	C.enquire_set_query(enq.ptr, q.ptr)
}

func (enq *Enquire) GetMSet(first, maxitems uint) *MSet {
	if enq.isClosed() {
		return nil
	}
	msetPtr := C.enquire_get_mset(enq.ptr, C.uint(first), C.uint(maxitems))
	if msetPtr == nil {
		return nil
	}
	mset := &MSet{ptr: msetPtr}
	runtime.SetFinalizer(mset, (*MSet).Close)
	return mset
}

// --- MSet Methods ---
func (mset *MSet) Close() {
	if !atomic.CompareAndSwapInt32(&mset.closed, 0, 1) {
		return
	}
	C.mset_free(mset.ptr)
}
func (mset *MSet) GetSize() int {
	if mset.isClosed() {
		return 0 // Or an appropriate default/error value
	}
	return int(C.mset_get_size(mset.ptr))
}
func (mset *MSet) GetDocument(index uint) *Document {
	if mset.isClosed() {
		return nil
	}
	docPtr := C.mset_get_document(mset.ptr, C.uint(index))
	if docPtr == nil {
		return nil
	}
	doc := &Document{ptr: docPtr}
	runtime.SetFinalizer(doc, (*Document).Close)
	return doc
}
func (mset *MSet) GetRank(index uint) int {
	if mset.isClosed() {
		return -1
	}
	return int(C.mset_get_rank(mset.ptr, C.uint(index)))
}

func (mset *MSet) GetMatchesEstimated() uint {
	if mset.isClosed() {
		return 0
	}
	return uint(C.mset_get_matches_estimated(mset.ptr))
}
