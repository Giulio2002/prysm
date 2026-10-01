//go:build bendssz

package bendssz

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Random valid objects: fill each registered Prysm type with random content that respects its
// ssz-size / ssz-max tags (empty lists, full small lists, random bit lists, random bytes), serialise
// it with the type's own fastssz MarshalSSZ, and require the C path to decode those bytes to an
// object equal to the generated one (proto.Equal, reflect.DeepEqual after the empty/nil
// normalisation done by fastssz, and an identical re-marshal).

type gen struct{ rng *rand.Rand }

func tagInts(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	for _, p := range strings.Split(s, ",") {
		if p == "?" {
			out = append(out, -1)
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			panic(err)
		}
		if n > 1<<40 {
			n = 1 << 40
		}
		out = append(out, int(n))
	}
	return out
}

func (g *gen) length(max int) int {
	if max > 6 {
		max = 6
	}
	switch g.rng.Intn(8) {
	case 0:
		return 0
	case 1:
		return max
	}
	return g.rng.Intn(max + 1)
}

func (g *gen) bytes(n int) []byte {
	b := make([]byte, n)
	g.rng.Read(b)
	if n > 0 && g.rng.Intn(10) == 0 {
		for i := range b { // runs of 0x00 / 0xff
			b[i] = []byte{0, 0xff}[g.rng.Intn(2)]
		}
	}
	return b
}

func (g *gen) fill(v reflect.Value, size, max []int) {
	t := v.Type()
	switch t.Kind() {
	case reflect.Uint64:
		x := g.rng.Uint64()
		switch g.rng.Intn(6) {
		case 0:
			x = 0
		case 1:
			x = ^uint64(0)
		}
		v.SetUint(x)
	case reflect.Uint32, reflect.Uint16, reflect.Uint8:
		v.SetUint(uint64(g.rng.Uint32()) & (1<<(t.Bits()) - 1))
	case reflect.Bool:
		v.SetBool(g.rng.Intn(2) == 0)
	case reflect.Ptr:
		n := reflect.New(t.Elem())
		g.fillStruct(n.Elem())
		v.Set(n)
	case reflect.Slice:
		e := t.Elem()
		dim := func(l []int, i int) int {
			if i < len(l) {
				return l[i]
			}
			return -1
		}
		switch {
		case e.Kind() == reflect.Uint8 && t.Name() == "Bitlist":
			k := g.rng.Intn(70)
			if m := dim(max, 0); m > 0 && k > m {
				k = m
			}
			b := make([]byte, k/8+1)
			g.rng.Read(b)
			for i := k; i < 8*len(b); i++ { // clear every bit from k up, then set the delimiter
				b[i/8] &^= 1 << (i % 8)
			}
			b[k/8] |= 1 << (k % 8)
			v.SetBytes(b)
		case e.Kind() == reflect.Uint8:
			n := dim(size, 0)
			if n < 0 {
				m := dim(max, 0)
				if m > 70 {
					m = 70
				}
				n = g.rng.Intn(m + 1)
			}
			b := g.bytes(n)
			if t.Name() == "Bitvector4" { // the 4 padding bits of a Bitvector[4] must be zero
				b[0] &= 0x0f
			}
			v.SetBytes(b)
		case e.Kind() == reflect.Uint64:
			n := dim(size, 0)
			if n < 0 {
				n = g.length(dim(max, 0))
			}
			s := reflect.MakeSlice(t, n, n)
			for i := 0; i < n; i++ {
				g.fill(s.Index(i), nil, nil)
			}
			v.Set(s)
		case e.Kind() == reflect.Slice && e.Elem().Kind() == reflect.Uint8:
			outer, inner := dim(size, 0), dim(size, 1)
			n := outer
			if n < 0 {
				n = g.length(dim(max, 0))
			}
			s := reflect.MakeSlice(t, n, n)
			for i := 0; i < n; i++ {
				m := inner
				if m < 0 {
					m = g.rng.Intn(60)
				}
				s.Index(i).SetBytes(g.bytes(m))
			}
			v.Set(s)
		case e.Kind() == reflect.Ptr:
			n := dim(size, 0)
			if n < 0 {
				n = g.length(dim(max, 0))
			}
			s := reflect.MakeSlice(t, n, n)
			for i := 0; i < n; i++ {
				g.fill(s.Index(i), nil, nil)
			}
			v.Set(s)
		default:
			panic("random: unsupported " + t.String())
		}
	default:
		panic("random: unsupported " + t.String())
	}
}

func (g *gen) fillStruct(v reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		g.fill(v.Field(i), tagInts(f.Tag.Get("ssz-size")), tagInts(f.Tag.Get("ssz-max")))
	}
}

func TestRandomObjects(t *testing.T) {
	n := 400
	if s := os.Getenv("BENDSSZ_RANDOM"); s != "" {
		fmt.Sscanf(s, "%d", &n)
	}
	g := &gen{rng: rand.New(rand.NewSource(7))}
	total := 0
	for _, e := range order {
		reps := n
		if e.name == "BeaconState" || e.name == "SyncCommittee" || e.name == "DataColumnSidecar" {
			reps = n / 20
		}
		for r := 0; r < reps; r++ {
			obj := reflect.New(e.typ)
			g.fillStruct(obj.Elem())
			in, err := obj.Interface().(marshaler).MarshalSSZ()
			if err != nil {
				t.Fatalf("%s: generated object does not serialise: %v", e.name, err)
			}
			acc, msg := compare(e, in)
			if !acc || msg != "" {
				t.Errorf("%s random object %d (%d bytes): accepted=%v %s", e.name, r, len(in), acc, msg)
				break
			}
			total++
		}
	}
	t.Logf("%d random valid objects over %d types, all decode to equal objects through C", total, len(order))
}
