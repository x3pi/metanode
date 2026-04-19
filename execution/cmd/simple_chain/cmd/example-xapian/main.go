package main

import (
	"fmt"
	"log"
	"os"

	"github.com/meta-node-blockchain/meta-node/pkg/goxapian"
	// Thay đổi đường dẫn import này cho đúng với module của bạn
)

func main() {
	dbPath := "./xapian-go-example"
	os.RemoveAll(dbPath)

	fmt.Printf("Mở database tại: %s\n", dbPath)

	db, err := goxapian.NewWritableDatabase(dbPath)
	if err != nil {
		log.Fatalf("Lỗi mở database: %v", err)
	}
	defer db.Close()

	// 1. Lập chỉ mục (Indexing)
	fmt.Println("\n--- Bắt đầu lập chỉ mục ---")
	doc1 := goxapian.NewDocument()
	defer doc1.Close()
	doc1.SetData("Đây là nội dung về AI và lập trình Go.")
	doc1.AddTerm("ai")
	doc1.AddTerm("go")
	doc1.AddTerm("lập")
	doc1.AddTerm("trình")

	doc2 := goxapian.NewDocument()
	defer doc2.Close()
	doc2.SetData("Xapian là một công cụ tìm kiếm mã nguồn mở tuyệt vời.")
	doc2.AddTerm("xapian")
	doc2.AddTerm("tìm")
	doc2.AddTerm("kiếm")
	doc2.AddTerm("mở")
	doc2.AddTerm("công")
	doc2.AddTerm("cụ")

	doc3 := goxapian.NewDocument()
	defer doc3.Close()
	doc3.SetData("Tìm kiếm và phân tích dữ liệu với Go và Xapian.")
	doc3.AddTerm("tìm")
	doc3.AddTerm("kiếm")
	doc3.AddTerm("phân")
	doc3.AddTerm("tích")
	// **ĐÃ SỬA**: Lập chỉ mục với một tiền tố. 'L' là mã đại diện cho 'language'.
	// Thuật ngữ thực sự được lưu trữ sẽ là "Lgo".
	doc3.AddTerm("Lgo")
	doc3.AddTerm("xapian")

	db.AddDocument(doc1)
	db.AddDocument(doc2)
	db.AddDocument(doc3)
	db.Commit()
	fmt.Printf("Lập chỉ mục hoàn tất. Tổng số document: %d\n", db.GetDocCount())

	// 2. Tìm kiếm (Searching)
	fmt.Println("\n--- Bắt đầu tìm kiếm với Boolean Logic ---")
	qp := goxapian.NewQueryParser()
	defer qp.Close()
	qp.SetDatabase(db)
	qp.SetStemmer("vietnamese")

	// **ĐÃ SỬA**: Đăng ký tiền tố với QueryParser.
	// Ánh xạ tên trường "language" mà người dùng sẽ gõ tới mã tiền tố "L"
	// mà chúng ta đã sử dụng khi lập chỉ mục.
	qp.AddPrefix("language", "L")

	features := []goxapian.QueryParserFeature{
		goxapian.FeatureBoolean,
		goxapian.FeatureBooleanAnyCase,
		goxapian.FeaturePhrase,
	}

	// --- Ví dụ 1: Tìm kiếm AND ---
	// Tìm các tài liệu chứa CẢ "go" trong trường "language" VÀ "xapian"
	// Truy vấn này bây giờ sẽ hoạt động đúng.
	fmt.Println("\n--- Test 1: (language:go AND xapian) ---")
	executeQuery(db, qp, "language:go AND xapian", features...)

	// --- Ví dụ 2: Tìm kiếm OR (với toán tử chữ thường) ---
	// Tìm các tài liệu chứa "ai" HOẶC "mở"
	fmt.Println("\n--- Test 2: (ai or mở) ---")
	executeQuery(db, qp, "ai or mở", features...)

	// --- Ví dụ 3: Tìm kiếm phức hợp ---
	// Tìm (chứa "kiếm") VÀ (chứa "go" - thuật ngữ thường HOẶC "công")
	// Lưu ý: `go` ở đây sẽ khớp với doc1, không phải doc3 vì nó không có tiền tố.
	fmt.Println(`\n--- Test 3: kiếm AND (go OR công) ---`)
	executeQuery(db, qp, "kiếm AND (go OR công)", features...)

	// --- Ví dụ 4: Tìm kiếm với NOT ---
	// Tìm (chứa "tìm") NHƯNG KHÔNG chứa "công"
	fmt.Println(`\n--- Test 4: tìm NOT công ---`)
	executeQuery(db, qp, "tìm NOT công", features...)

}

// **ĐÃ SỬA**: Cập nhật hàm trợ giúp để nhận và truyền các cờ
func executeQuery(db *goxapian.Database, qp *goxapian.QueryParser, queryStr string, features ...goxapian.QueryParserFeature) {
	fmt.Printf("Thực thi truy vấn: '%s'\n", queryStr)
	// Truyền các cờ vào hàm ParseQuery
	query := qp.ParseQuery(queryStr, features...)
	if query == nil {
		log.Printf("Lỗi khi parse query: '%s'", queryStr)
		return
	}
	defer query.Close()

	enquire := db.Enquire()
	defer enquire.Close()
	enquire.SetQuery(query)

	mset := enquire.GetMSet(0, 10)
	if mset == nil {
		log.Println("Lỗi khi lấy MSet")
		return
	}
	defer mset.Close()

	fmt.Printf("=> Tìm thấy %d kết quả:\n", mset.GetSize())
	for i := 0; i < mset.GetSize(); i++ {
		doc := mset.GetDocument(uint(i))
		if doc != nil {
			fmt.Printf("  - Kết quả %d: %s\n", i+1, doc.GetData())
			doc.Close()
		}
	}
}
