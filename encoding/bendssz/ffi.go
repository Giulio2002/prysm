//go:build bendssz

package bendssz

/*
#cgo LDFLAGS: ${SRCDIR}/lib/libbendssz.a -lpthread -lm
#include "bendssz.h"
*/
import "C"

import (
	"sync"
	"unsafe"
)

// mu serialises the library: the Bend runtime is one global heap with one
// evaluation stack. It is held from the call until the Go object is built,
// because the C stream is only valid until the next call.
var mu sync.Mutex

// call runs one decode; the caller holds mu. The returned stream aliases C memory.
func call(id uint32, in []byte) (stream []byte, rc int) {
	var out *C.uint8_t
	var n C.uint64_t
	var p *C.uint8_t
	if len(in) > 0 {
		p = (*C.uint8_t)(unsafe.Pointer(&in[0]))
	}
	r := C.bendssz_call(C.uint32_t(id), p, C.uint32_t(len(in)), &out, &n)
	if r != 0 || out == nil {
		return nil, int(r)
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(out)), int(n)), 0
}

func heapWords() uint64 { return uint64(C.bendssz_heap_words()) }
