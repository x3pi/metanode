#!/bin/bash
export PATH="$HOME/go/bin:$GOPATH/bin:$PATH"

if ! command -v protoc-gen-go-vtproto &> /dev/null
then
    echo "Installing protoc-gen-go-vtproto..."
    go install github.com/planetscale/vtprotobuf/cmd/protoc-gen-go-vtproto@latest
fi

protoc --go_out=. --go_opt=paths=source_relative \
       --go-vtproto_out=. --go-vtproto_opt=paths=source_relative,features=unmarshal \
       *.proto