# Tạo 1 key (mặc định)
go run main.go
# Tạo 5 key
go run main.go -count 5
# Tạo 10 key và lưu ra file JSON
go run main.go -count 10 -output keys.json
# Khôi phục public key và address từ private key
go run main.go -recover 2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b