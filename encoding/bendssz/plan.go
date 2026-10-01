//go:build bendssz

package bendssz

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unsafe"
)

// The C object is a stream of little endian 32-bit words: the Bend runtime's
// flat dump of the decoded object (bend-ssz src/obj.bend, "the word-granular
// flat stream", the word twin of its structural value dump). Leaves in SSZ
// field order, each padded to whole words:
//
//	boolean, uint8/16/32           one word
//	uint64                         two words (8 bytes)
//	Vector / ByteVector / Bitvector the bytes, padded to a word, no count
//	List of fixed-size bytes / uints   u32 byte length, then the bytes (padded)
//	ByteList                       u32 byte length, then the bytes (padded)
//	Bitlist                        u32 bit count, then ceil(bits/8) bytes (padded; no delimiter bit)
//	List of containers / of lists  u32 element count, then the elements
//	Vector of containers           the elements, no count
//
// A plan is compiled once per Go struct type from the protobuf struct tags
// (ssz-size, ssz-max) and the field kinds; running it builds the struct from
// the stream with no reflection on the hot path (offsets and unsafe stores).

type decoder struct {
	buf  []byte
	pos  int
	slab []byte
	bad  bool
}

func (d *decoder) take(n int) []byte {
	if n < 0 || d.pos+n > len(d.buf) {
		d.bad = true
		return nil
	}
	b := d.buf[d.pos : d.pos+n]
	d.pos += n
	return b
}

func pad4(n int) int { return (n + 3) &^ 3 }

// takePad takes n bytes of data that the stream pads to a whole word.
func (d *decoder) takePad(n int) []byte {
	b := d.take(pad4(n))
	if b == nil {
		return nil
	}
	return b[:n]
}

func (d *decoder) u32() int {
	if d.pos+4 > len(d.buf) {
		d.bad = true
		return 0
	}
	v := binary.LittleEndian.Uint32(d.buf[d.pos:])
	d.pos += 4
	return int(v)
}

// alloc hands out n bytes of the per-call slab (capacity clipped to n, so an
// append on the result never reaches a neighbour); big requests get their own
// allocation.
func (d *decoder) alloc(n int) []byte {
	if n > len(d.slab) {
		return make([]byte, n)
	}
	b := d.slab[:n:n]
	d.slab = d.slab[n:]
	return b
}

type op func(d *decoder, p unsafe.Pointer)

type structPlan struct {
	typ  reflect.Type
	size uintptr
	offs []uintptr
	ops  []op
}

func (sp *structPlan) run(d *decoder, p unsafe.Pointer) {
	for i, o := range sp.ops {
		o(d, unsafe.Add(p, sp.offs[i]))
		if d.bad {
			return
		}
	}
}

var plans = map[reflect.Type]*structPlan{}

func planFor(t reflect.Type) *structPlan {
	if sp, ok := plans[t]; ok {
		return sp
	}
	sp := &structPlan{typ: t, size: t.Size()}
	plans[t] = sp
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // protoimpl state, sizeCache, unknownFields
			continue
		}
		sz := splitTag(f.Tag.Get("ssz-size"))
		o := fieldOp(f.Type, sz)
		sp.offs = append(sp.offs, f.Offset)
		sp.ops = append(sp.ops, o)
	}
	return sp
}

func splitTag(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// dim: the fixed length of one dimension, or -1 for a list ("?" or absent).
func dim(sz []string, i int) int {
	if i >= len(sz) || sz[i] == "?" {
		return -1
	}
	n, err := strconv.Atoi(sz[i])
	if err != nil {
		panic("bendssz: bad ssz-size " + strings.Join(sz, ","))
	}
	return n
}

var le64 = func() bool { // the raw copy of []uint64 needs a little endian host
	var x uint16 = 1
	return *(*byte)(unsafe.Pointer(&x)) == 1
}()

func fieldOp(t reflect.Type, sz []string) op {
	switch t.Kind() {
	case reflect.Uint64:
		return func(d *decoder, p unsafe.Pointer) {
			if b := d.take(8); b != nil {
				*(*uint64)(p) = binary.LittleEndian.Uint64(b)
			}
		}
	case reflect.Uint32:
		return func(d *decoder, p unsafe.Pointer) { *(*uint32)(p) = uint32(d.u32()) }
	case reflect.Uint16:
		return func(d *decoder, p unsafe.Pointer) { *(*uint16)(p) = uint16(d.u32()) }
	case reflect.Uint8:
		return func(d *decoder, p unsafe.Pointer) { *(*uint8)(p) = uint8(d.u32()) }
	case reflect.Bool:
		return func(d *decoder, p unsafe.Pointer) { *(*bool)(p) = d.u32() != 0 }
	case reflect.Ptr:
		if t.Elem().Kind() != reflect.Struct {
			panic("bendssz: unsupported pointer field " + t.String())
		}
		sp := planFor(t.Elem())
		return func(d *decoder, p unsafe.Pointer) {
			n := reflect.New(sp.typ).UnsafePointer()
			sp.run(d, n)
			*(*unsafe.Pointer)(p) = n
		}
	case reflect.Slice:
		return sliceOp(t, sz)
	}
	panic("bendssz: unsupported field type " + t.String())
}

func sliceOp(t reflect.Type, sz []string) op {
	e := t.Elem()
	switch {
	case e.Kind() == reflect.Uint8:
		if t.Name() == "Bitlist" {
			return bitlistOp
		}
		if n := dim(sz, 0); n >= 0 {
			return bytesFixedOp(n)
		}
		return bytesListOp
	case e.Kind() == reflect.Uint64:
		if n := dim(sz, 0); n >= 0 {
			return u64sFixedOp(n)
		}
		return u64sListOp
	case e.Kind() == reflect.Slice && e.Elem().Kind() == reflect.Uint8:
		outer, inner := dim(sz, 0), dim(sz, 1)
		switch {
		case outer < 0 && inner >= 0:
			return vecListOp(inner)
		case outer >= 0 && inner >= 0:
			return vecVecOp(outer, inner)
		case outer < 0 && inner < 0:
			return bytesListListOp
		}
	case e.Kind() == reflect.Ptr && e.Elem().Kind() == reflect.Struct:
		sp := planFor(e.Elem())
		return structListOp(sp, dim(sz, 0))
	}
	panic("bendssz: unsupported slice field " + t.String() + " ssz-size=" + strings.Join(sz, ","))
}

func bytesFixedOp(n int) op {
	return func(d *decoder, p unsafe.Pointer) {
		src := d.takePad(n)
		if src == nil || n == 0 {
			*(*[]byte)(p) = nil
			return
		}
		b := d.alloc(n)
		copy(b, src)
		*(*[]byte)(p) = b
	}
}

func bytesListOp(d *decoder, p unsafe.Pointer) {
	src := d.takePad(d.u32())
	if d.bad {
		return
	}
	// like fastssz, an empty list is a non-nil empty slice
	b := d.alloc(len(src))
	copy(b, src)
	*(*[]byte)(p) = b
}

// A Go Bitlist is the bytes with a delimiter bit set just past the last bit.
func bitlistOp(d *decoder, p unsafe.Pointer) {
	k := d.u32()
	src := d.takePad((k + 7) / 8)
	if d.bad {
		return
	}
	b := d.alloc(k/8 + 1)
	copy(b, src)
	if k%8 == 0 {
		b[k/8] = 1
	} else {
		b[k/8] |= 1 << (k % 8)
	}
	*(*[]byte)(p) = b
}

func u64sFixedOp(n int) op {
	return func(d *decoder, p unsafe.Pointer) { u64s(d, p, n) }
}

func u64sListOp(d *decoder, p unsafe.Pointer) {
	n := d.u32()
	if n%8 != 0 {
		d.bad = true
		return
	}
	u64s(d, p, n/8)
}

func u64s(d *decoder, p unsafe.Pointer, n int) {
	src := d.take(n * 8)
	if d.bad {
		return
	}
	s := make([]uint64, n)
	if n == 0 {
		*(*[]uint64)(p) = s
		return
	}
	if le64 {
		copy(unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), n*8), src)
	} else {
		for i := range s {
			s[i] = binary.LittleEndian.Uint64(src[i*8:])
		}
	}
	*(*[]uint64)(p) = s
}

// carve cuts c slices of n bytes out of one slab region holding the stream bytes src.
func carve(d *decoder, p unsafe.Pointer, src []byte, c, n int) {
	if d.bad {
		return
	}
	if c == 0 {
		*(*[][]byte)(p) = [][]byte{}
		return
	}
	flat := d.alloc(c * n)
	copy(flat, src)
	out := make([][]byte, c)
	for i := range out {
		out[i] = flat[i*n : (i+1)*n : (i+1)*n]
	}
	*(*[][]byte)(p) = out
}

// List of fixed-size byte vectors: elements whose size is a multiple of 4
// (Bytes32, Bytes48, ...) are packed and prefixed by their byte length, the
// others by their count.
func vecListOp(n int) op {
	return func(d *decoder, p unsafe.Pointer) {
		c := d.u32()
		if n%4 == 0 {
			if n == 0 || c%n != 0 {
				d.bad = true
				return
			}
			c /= n
		}
		carvePad(d, p, c, n)
	}
}

func vecVecOp(m, n int) op {
	return func(d *decoder, p unsafe.Pointer) { carvePad(d, p, m, n) }
}

// carvePad reads c elements of n bytes: contiguous when n is a multiple of 4,
// else each element padded to a word.
func carvePad(d *decoder, p unsafe.Pointer, c, n int) {
	if d.bad || c < 0 || c > len(d.buf) {
		d.bad = true
		return
	}
	if n%4 == 0 {
		carve(d, p, d.take(c*n), c, n)
		return
	}
	if c == 0 {
		*(*[][]byte)(p) = [][]byte{}
		return
	}
	flat := d.alloc(c * n)
	out := make([][]byte, c)
	for i := range out {
		copy(flat[i*n:], d.takePad(n))
		out[i] = flat[i*n : (i+1)*n : (i+1)*n]
	}
	*(*[][]byte)(p) = out
}

// List of byte lists (transactions): the count, then each element's length and bytes.
func bytesListListOp(d *decoder, p unsafe.Pointer) {
	c := d.u32()
	if d.bad || c > len(d.buf) {
		d.bad = true
		return
	}
	if c == 0 {
		*(*[][]byte)(p) = [][]byte{}
		return
	}
	out := make([][]byte, c)
	for i := range out {
		src := d.takePad(d.u32())
		if d.bad {
			return
		}
		b := d.alloc(len(src)) // like fastssz, an empty element is a non-nil empty slice
		copy(b, src)
		out[i] = b
	}
	*(*[][]byte)(p) = out
}

var emptyBase = unsafe.Pointer(&[0]int{})

type sliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

// List (count prefix) or Vector (fixed >= 0) of containers: one array holds
// the structs, one the pointers (Prysm's protobuf types hold []*T).
func structListOp(sp *structPlan, fixed int) op {
	sliceT := reflect.SliceOf(sp.typ)
	ptrSliceT := reflect.SliceOf(reflect.PointerTo(sp.typ))
	return func(d *decoder, p unsafe.Pointer) {
		c := fixed
		if c < 0 {
			c = d.u32()
		}
		if d.bad || c > len(d.buf) {
			d.bad = true
			return
		}
		if c == 0 {
			// like fastssz, an empty list is a non-nil empty slice
			*(*sliceHeader)(p) = sliceHeader{data: emptyBase, len: 0, cap: 0}
			return
		}
		arr := reflect.MakeSlice(sliceT, c, c).UnsafePointer()
		ptrs := reflect.MakeSlice(ptrSliceT, c, c).UnsafePointer()
		pp := unsafe.Slice((*unsafe.Pointer)(ptrs), c)
		for i := 0; i < c; i++ {
			e := unsafe.Add(arr, uintptr(i)*sp.size)
			pp[i] = e
			sp.run(d, e)
			if d.bad {
				return
			}
		}
		*(*sliceHeader)(p) = sliceHeader{ptrs, c, c}
	}
}

func (sp *structPlan) String() string { return fmt.Sprintf("plan(%s, %d fields)", sp.typ, len(sp.ops)) }
