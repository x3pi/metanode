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
	"unsafe"
)

// --- Struct Wrappers ---
type Database struct{ ptr C.xapian_database_t }
type Document struct{ ptr C.xapian_document_t }
type QueryParser struct{ ptr C.xapian_queryparser_t }
type Query struct{ ptr C.xapian_query_t }
type Enquire struct{ ptr C.xapian_enquire_t }
type MSet struct{ ptr C.xapian_mset_t }

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
	if db.ptr != nil {
		C.database_close(db.ptr)
		db.ptr = nil
	}
}
func (db *Database) GetDocCount() uint {
	if db.ptr == nil {
		return 0
	}
	return uint(C.database_get_doccount(db.ptr))
}
func (db *Database) AddDocument(doc *Document) uint {
	if db.ptr == nil || doc.ptr == nil {
		return 0 // Or an appropriate error code/value
	}
	return uint(C.database_add_document(db.ptr, doc.ptr))
}
func (db *Database) ReplaceDocumentByTerm(uniqueTerm string, doc *Document) {
	if db.ptr == nil || doc.ptr == nil {
		return
	}
	cTerm := C.CString(uniqueTerm)
	defer C.free(unsafe.Pointer(cTerm))
	C.database_replace_document_by_term(db.ptr, cTerm, doc.ptr)
}
func (db *Database) Commit() {
	if db.ptr == nil {
		return
	}
	C.database_commit(db.ptr)
}
func (db *Database) Enquire() *Enquire {
	if db.ptr == nil {
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
	if db.ptr == nil {
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
	if doc.ptr != nil {
		C.document_free(doc.ptr)
		doc.ptr = nil
	}
}
func (doc *Document) SetData(data string) {
	if doc.ptr == nil {
		return
	}
	cData := C.CString(data)
	defer C.free(unsafe.Pointer(cData))
	C.document_set_data(doc.ptr, cData)
}
func (doc *Document) AddTerm(term string) {
	if doc.ptr == nil {
		return
	}
	cTerm := C.CString(term)
	defer C.free(unsafe.Pointer(cTerm))
	C.document_add_term(doc.ptr, cTerm)
}
func (doc *Document) GetData() string {
	if doc.ptr == nil {
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
	if qp.ptr != nil {
		C.queryparser_free(qp.ptr)
		qp.ptr = nil
	}
}
func (qp *QueryParser) SetDatabase(db *Database) {
	if qp.ptr == nil || db.ptr == nil {
		return
	}
	C.queryparser_set_database(qp.ptr, db.ptr)
}
func (qp *QueryParser) SetStemmer(lang string) {
	if qp.ptr == nil {
		return
	}
	cLang := C.CString(lang)
	defer C.free(unsafe.Pointer(cLang))
	C.queryparser_set_stemming_language(qp.ptr, cLang)
}
func (qp *QueryParser) SetDefaultOp(op QueryOp) {
	if qp.ptr == nil {
		return
	}
	C.queryparser_set_default_op(qp.ptr, C.xapian_query_op(op))
}

func (qp *QueryParser) AddPrefix(field, prefix string) {
	if qp.ptr == nil {
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
	if qp.ptr == nil {
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
	if q.ptr != nil {
		C.query_free(q.ptr)
		q.ptr = nil
	}
}

// --- Enquire Methods ---
func (enq *Enquire) Close() {
	if enq.ptr != nil {
		C.enquire_free(enq.ptr)
		enq.ptr = nil
	}
}
func (enq *Enquire) SetQuery(q *Query) {
	if enq.ptr == nil || q.ptr == nil {
		return
	}
	C.enquire_set_query(enq.ptr, q.ptr)
}

func (enq *Enquire) GetMSet(first, maxitems uint) *MSet {
	if enq.ptr == nil {
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
	if mset.ptr != nil {
		C.mset_free(mset.ptr)
		mset.ptr = nil
	}
}
func (mset *MSet) GetSize() int {
	if mset.ptr == nil {
		return 0 // Or an appropriate default/error value
	}
	return int(C.mset_get_size(mset.ptr))
}
func (mset *MSet) GetDocument(index uint) *Document {
	if mset.ptr == nil {
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
	if mset.ptr == nil {
		return -1
	}
	return int(C.mset_get_rank(mset.ptr, C.uint(index)))
}

func (mset *MSet) GetMatchesEstimated() uint {
	if mset.ptr == nil {
		return 0
	}
	return uint(C.mset_get_matches_estimated(mset.ptr))
}
