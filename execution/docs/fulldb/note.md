// Thêm term để filter chính xác (giống WHERE category = 'phone')
IXapian(0x106).addTerm("products", 1, "category:phone");
IXapian(0x106).addTerm("products", 2, "category:phone");

// Index full-text để search (giống LIKE '%iPhone%')
IXapian(0x106).indexText("products", 1, "iPhone Apple smartphone", 1, "");
IXapian(0x106).indexText("products", 2, "Pixel Google smartphone", 1, "");

// Thêm value để sort theo giá (giống ORDER BY price)
IXapian(0x106).addValue("products", 1, 0, "999", true);
IXapian(0x106).addValue("products", 2, 0, "799", true);

// CUỐI CÙNG: commit để ghi xuống disk
IXapian(0x106).commit("products");
