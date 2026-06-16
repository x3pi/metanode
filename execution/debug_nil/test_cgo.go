//go:build ignore

package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"unsafe"
)

func main() {
	var cPtr unsafe.Pointer = nil
	length := C.int(5)

	fmt.Println("Testing C.GoBytes with nil pointer and length > 0")
	bytes := C.GoBytes(cPtr, length)
	fmt.Printf("Len: %d, Cap: %d\n", len(bytes), cap(bytes))
}
