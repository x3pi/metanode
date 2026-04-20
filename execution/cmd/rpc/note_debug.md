"Trái tim" của việc giải mã Custom Errors trong Solidity (được giới thiệu từ bản 0.8.4).

4 bytes đầu: Selector của lỗi Error(string), luôn là 0x08c379a0.
32 bytes tiếp theo: Offset (vị trí bắt đầu của dữ liệu chuỗi).
32 bytes tiếp theo: Độ dài của chuỗi (Length).
Phần còn lại: Dữ liệu thực tế của chuỗi (Data).


### source code 
phải giống k thừa dấu , dáu cách nào hết, nếu thừa thiếu dẫn đến tạo ra bytecode lỗi