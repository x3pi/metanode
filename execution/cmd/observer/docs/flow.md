
Chain A nhận giao dịch từ người dùng

Tx1 : {
    "from": "W-A1",
    "to": "SM XC",
    "value": 100,
    "data": { "recipient": "WC1", Chain: B},
}
.....
Tx10 : {
    "from": "W-A10",
    "to": "SM XC",
    "value": 100,1
    "data": {"recipient": "WC10",  Chain: B},
}

+ Đốt 
+ Mọi giao dịch xuyên chain sẽ qua 1 địa chỉ chung. Cá dữ liệu khác sẽ đưa vào data để phân loại



Đại sứ quán:

+ Đại sứ quán quét block
+ Giả sử block 10 có 10 giao dịch xuyên chain như sau. Thứ tự nhiều đại sứ quán sẽ có thứ tự giao dịch giống nhau. Nên cho có thể gán luồng ID sẽ giống nhau trên tất cả đại sứ quán
+ Biết người dùng gửi chain B sẽ ứng với Kenh id 1


Tx10 : {
    "from": "W-DSQ10",
    "to": "SM XC",
    "value": 100,1
    "data": {"call tract": "WC10"},
}



+ SM-B1-A1 conrtact fake để xử lý song song. Cần 1 contract thật để đăng ký vào config gồm Chain B
+ frome sẽ được quy đinh theo byte kenh ID + luồng ID để chạy song song
+ Thay check bls bằng cho vào hàng đợi chek đủ chữ kỹ của đại sứ quan theo đăng ký ở contract



+ Chain thì cần biết kenh ID tương ứng với đại sứ quán nào gửi (Đọc song song)
+ Còn đại sứ quán thì trước gửi vào contract giờ golang bắt xử lý (Ghi bắt golang xử lý)







Confim:


Tx1 : {
    "from": "W-DSQ1",
    "to": "SM XC",
    "value": 100,
    "data": { "Confim": "W-A1"},
}
.....


Tx10 : {
    "from": "W-DSQ10",
    "to": "SM XC",
    "value": 100,1
    "data": {"Confim": "W-A10", Txh , value },
}

Lỗi, 
+ Relative address : from W-DSQ1 , người đã gửi  W-A10



