protoc --plugin=protoc-gen-ts=$(which protoc-gen-ts) \
  --plugin=protoc-gen-grpc-web=$(which protoc-gen-grpc-web) \
  --js_out=import_style=commonjs,binary:./ \
  --grpc-web_out=import_style=typescript,mode=grpcwebtext:./ \
  --ts_out=./ \
  --proto_path=.// transaction.proto


protoc --plugin=protoc-gen-ts=$(which protoc-gen-ts) \
  --plugin=protoc-gen-grpc-web=$(which protoc-gen-grpc-web) \
  --js_out=import_style=commonjs,binary:./ \
  --grpc-web_out=import_style=typescript,mode=grpcwebtext:./ \
  --ts_out=./ \
  --proto_path=./ message.proto