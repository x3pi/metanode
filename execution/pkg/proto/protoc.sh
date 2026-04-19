#!/bin/bash
# Ensure GOPATH/bin is in PATH for protoc-gen-go
export PATH="$HOME/go/bin:$GOPATH/bin:$PATH"

# Run protoc
protoc --go_out=. --go_opt=paths=source_relative *.proto