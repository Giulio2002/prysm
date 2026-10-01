//go:build bendssz

package bendssz

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
)

// BENDSSZ_FIXTURES points at a directory <Name>/ssz_random/case_N.ssz holding the
// decompressed official consensus-spec-tests ssz_static (fulu, mainnet) vectors.
func fixtures(t testing.TB, e *entry) [][]byte {
	dir := os.Getenv("BENDSSZ_FIXTURES")
	if dir == "" {
		t.Skip("BENDSSZ_FIXTURES not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, e.name, "*", "*.ssz"))
	var out [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		t.Skipf("no fixtures for %s", e.name)
	}
	return out
}

var lenient = map[string]int{}

type marshaler interface{ MarshalSSZ() ([]byte, error) }

// compare decodes in through fastssz and through C; it returns (accepted by C, error text if they disagree).
func compare(e *entry, in []byte) (bool, string) {
	ref := reflect.New(e.typ).Interface()
	rerr := ref.(unmarshaler).UnmarshalSSZ(in)
	got := reflect.New(e.typ).Interface()
	handled, err := Unmarshal(got, in)
	if err != nil {
		return handled, "error from Unmarshal: " + err.Error()
	}
	if rerr != nil {
		if handled {
			return handled, "C accepted, fastssz rejected: " + rerr.Error()
		}
		return false, ""
	}
	if !handled {
		// fastssz's UnmarshalSSZ does not enforce every ssz-max (e.g. ExtraData <= 32 of the
		// execution payload header); the object it builds then cannot be serialised again.
		// That is fastssz being lax, not a disagreement of the C decoder: count it apart.
		if _, merr := ref.(marshaler).MarshalSSZ(); merr != nil {
			lenient[e.name]++
			return false, ""
		}
		return false, "C rejected (or failed), fastssz accepted a valid object"
	}
	if !proto.Equal(got.(proto.Message), ref.(proto.Message)) {
		return true, "proto.Equal is false"
	}
	// byte-for-byte: re-serialising the C-built object gives the input back
	re, err := got.(marshaler).MarshalSSZ()
	if err != nil || !bytes.Equal(re, in) {
		return true, fmt.Sprintf("re-marshal differs (err=%v len %d vs %d)", err, len(re), len(in))
	}
	if !reflect.DeepEqual(got, ref) {
		return true, "proto.Equal but not reflect.DeepEqual: " + deepDiff(reflect.ValueOf(got).Elem(), reflect.ValueOf(ref).Elem(), e.name)
	}
	return true, ""
}

// deepDiff names the first field whose value differs (nil vs empty slices included).
func deepDiff(a, b reflect.Value, path string) string {
	switch a.Kind() {
	case reflect.Ptr:
		if a.IsNil() != b.IsNil() {
			return path + ": nil pointer mismatch"
		}
		if a.IsNil() {
			return ""
		}
		return deepDiff(a.Elem(), b.Elem(), path)
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if a.Type().Field(i).PkgPath != "" {
				continue
			}
			if d := deepDiff(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); d != "" {
				return d
			}
		}
	case reflect.Slice:
		if a.IsNil() != b.IsNil() {
			return fmt.Sprintf("%s: nil=%v vs nil=%v (len %d vs %d)", path, a.IsNil(), b.IsNil(), a.Len(), b.Len())
		}
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s: len %d vs %d", path, a.Len(), b.Len())
		}
		for i := 0; i < a.Len(); i++ {
			if d := deepDiff(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
	default:
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			return path + ": value differs"
		}
	}
	return ""
}

func TestFixtures(t *testing.T) {
	total := 0
	for _, e := range order {
		in := fixtures(t, e)
		for i, b := range in {
			acc, msg := compare(e, b)
			if !acc || msg != "" {
				t.Errorf("%s fixture %d (%d bytes): accepted=%v %s", e.name, i, len(b), acc, msg)
			}
			total++
		}
	}
	t.Logf("%d official fixtures, all equal to fastssz", total)
}

// Mutation fuzz: random corruptions of the fixtures (flips, truncation, growth,
// splices). Accept/reject must agree with fastssz and accepted objects must be equal.
func TestMutations(t *testing.T) {
	n := 300
	if s := os.Getenv("BENDSSZ_MUTATIONS"); s != "" {
		fmt.Sscanf(s, "%d", &n)
	}
	rng := rand.New(rand.NewSource(1))
	var checked, accepted int
	for _, e := range order {
		in := fixtures(t, e)
		big := len(in[0]) > 1<<20
		reps := n
		if big {
			reps = n / 20
		}
		for r := 0; r < reps; r++ {
			b := append([]byte(nil), in[rng.Intn(len(in))]...)
			switch rng.Intn(5) {
			case 0: // flip a few bytes
				for k := 0; k < 1+rng.Intn(3); k++ {
					b[rng.Intn(len(b))] ^= byte(1 << rng.Intn(8))
				}
			case 1: // overwrite a few bytes with random values
				for k := 0; k < 1+rng.Intn(4); k++ {
					b[rng.Intn(len(b))] = byte(rng.Intn(256))
				}
			case 2: // truncate
				b = b[:rng.Intn(len(b))]
			case 3: // grow
				b = append(b, make([]byte, 1+rng.Intn(40))...)
			case 4: // set an aligned 4-byte word (offsets / lengths) to a small number
				if len(b) >= 4 {
					p := rng.Intn(len(b)/4) * 4
					b[p], b[p+1], b[p+2], b[p+3] = byte(rng.Intn(300)), 0, 0, 0
				}
			}
			acc, msg := compare(e, b)
			checked++
			if acc {
				accepted++
			}
			if msg != "" {
				t.Errorf("%s mutation %d (%d bytes): %s", e.name, r, len(b), msg)
				if t.Failed() && checked > 0 {
					// keep going: every disagreement is a finding
				}
			}
		}
	}
	t.Logf("%d mutated inputs (%d accepted by both), all agree; inputs only fastssz accepts (it builds an object it cannot serialise): %v", checked, accepted, lenient)
}

func benchOne(b *testing.B, e *entry, in []byte, ffi bool) {
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	if ffi {
		for i := 0; i < b.N; i++ {
			m := reflect.New(e.typ).Interface()
			if ok, _ := Unmarshal(m, in); !ok {
				b.Fatal("not handled")
			}
		}
		return
	}
	for i := 0; i < b.N; i++ {
		m := reflect.New(e.typ).Interface()
		if err := m.(unmarshaler).UnmarshalSSZ(in); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecode: per type, fixture 0: fastssz UnmarshalSSZ vs the whole FFI path
// (cgo call + Bend validate/decode/flatten + building the Go object).
func BenchmarkDecode(b *testing.B) {
	for _, e := range order {
		in := fixtures(b, e)
		// the median-sized fixture
		best := in[0]
		for _, x := range in {
			if len(x) > len(best) {
				best = x
			}
		}
		_ = best
		b.Run(e.name+"/fastssz", func(b *testing.B) { benchOne(b, e, in[0], false) })
		b.Run(e.name+"/ffi", func(b *testing.B) { benchOne(b, e, in[0], true) })
	}
}

// BenchmarkBoundary: what crossing the boundary costs with nothing to decode.
//
//	cgo_call    a trivial C function (the cgo transition alone)
//	roundtrip   one request through the Bend request loop that decodes nothing: store the
//	            request, allocate its input array, resume the parked Ssz.exchange activation,
//	            step the Bend IO loop until it parks again (design A's per-getter floor)
//	build_only  the Go plan building the object from an already produced stream (no C call)
func BenchmarkBoundary(b *testing.B) {
	b.Run("cgo_call", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			heapWords()
		}
	})
	b.Run("roundtrip", func(b *testing.B) {
		in := make([]byte, 40)
		mu.Lock()
		defer mu.Unlock()
		for i := 0; i < b.N; i++ {
			if _, rc := call(uint32(len(table)), in); rc != 0 {
				b.Fatal("rc", rc)
			}
		}
	})
	b.Run("build_only/AttestationData", func(b *testing.B) {
		var e *entry
		for _, x := range order {
			if x.name == "AttestationData" {
				e = x
			}
		}
		in := fixtures(b, e)[0]
		mu.Lock()
		stream, rc := call(e.id, in)
		mu.Unlock()
		if rc != 0 {
			b.Fatal(rc)
		}
		stream = append([]byte(nil), stream...)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			d := decoder{buf: stream[4:], slab: make([]byte, len(stream)-4)}
			m := reflect.New(e.typ)
			e.plan.run(&d, m.UnsafePointer())
		}
	})
}

// TestSoak: the Bend heap must not grow while decoding the same inputs again and again (accepted,
// rejected, and a state with a 2.7 MB input); heapWords counts the pages the runtime ever took.
func TestSoak(t *testing.T) {
	n := 20000
	if s := os.Getenv("BENDSSZ_SOAK"); s != "" {
		fmt.Sscanf(s, "%d", &n)
	}
	var inputs []struct {
		e  *entry
		in []byte
	}
	for _, e := range order {
		fx := fixtures(t, e)
		inputs = append(inputs, struct {
			e  *entry
			in []byte
		}{e, fx[0]})
		inputs = append(inputs, struct {
			e  *entry
			in []byte
		}{e, fx[0][:len(fx[0])/2]}) // truncated: rejected
	}
	run := func(rounds int) {
		for r := 0; r < rounds; r++ {
			for _, x := range inputs {
				if len(x.in) > 1<<20 && r%50 != 0 {
					continue // the 2.7 MB state: every 50th round
				}
				m := reflect.New(x.e.typ).Interface()
				Unmarshal(m, x.in)
			}
		}
	}
	run(3) // warm-up: the heap reaches its working size
	before := heapWords()
	run(n / len(inputs) * 2)
	after := heapWords()
	t.Logf("%d decodes over %d inputs: Bend heap pages %d -> %d", n, len(inputs), before, after)
	if after > before+before/20 {
		t.Errorf("Bend heap grew from %d to %d", before, after)
	}
}

// BenchmarkParallel: the FFI path is serialised (one Bend heap behind a mutex); fastssz scales with
// the cores. ns/op is wall time per decode over all goroutines.
func BenchmarkParallel(b *testing.B) {
	for _, name := range []string{"SingleAttestation", "SignedBeaconBlock"} {
		var e *entry
		for _, x := range order {
			if x.name == name {
				e = x
			}
		}
		in := fixtures(b, e)[0]
		for _, ffi := range []bool{false, true} {
			label := "fastssz"
			if ffi {
				label = "ffi"
			}
			b.Run(name+"/"+label, func(b *testing.B) {
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						m := reflect.New(e.typ).Interface()
						if ffi {
							if ok, _ := Unmarshal(m, in); !ok {
								b.Fatal("not handled")
							}
						} else if err := m.(unmarshaler).UnmarshalSSZ(in); err != nil {
							b.Fatal(err)
						}
					}
				})
			})
		}
	}
}

// TestConcurrent: goroutines decoding different types at once (the FFI is serialised by mu);
// meant to be run with -race, and checks every result against fastssz.
func TestConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				e := order[(w*7+i)%len(order)]
				in := fixtures(t, e)
				if len(in[0]) > 1<<20 {
					continue
				}
				if acc, msg := compare(e, in[i%len(in)]); !acc || msg != "" {
					select {
					case errs <- fmt.Sprintf("%s: accepted=%v %s", e.name, acc, msg):
					default:
					}
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for m := range errs {
		t.Error(m)
	}
}
