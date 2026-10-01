//go:build bendssz

package bendssz

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

// Enabled reports whether this binary was built with the bendssz tag.
const Enabled = true

var log = logrus.WithField("prefix", "bendssz")

type entry struct {
	id   uint32
	name string
	typ  reflect.Type // the struct type
	plan *structPlan
	n    atomic.Uint64 // accepted through C
	rej  atomic.Uint64 // rejected by C
	bad  atomic.Uint64 // shadow mismatches
}

var (
	byType = map[reflect.Type]*entry{} // key: the pointer type
	order  []*entry
	shadow = envOn("BENDSSZ_SHADOW")
	strict = envOn("BENDSSZ_STRICT") // shadow also compares with reflect.DeepEqual
	off    = envOn("BENDSSZ_OFF")    // run the binary without the FFI path
	timing = envOn("BENDSSZ_TIMING") // time the C call and the Go build separately (two clock reads per decode)
)

// Types that reached Unmarshal without a registered plan (decoded by fastssz), counted for the report.
var (
	trackMiss = true
	missMu    sync.Mutex
	miss      = map[reflect.Type]*atomic.Uint64{}
)

func missed(t reflect.Type) {
	missMu.Lock()
	c := miss[t]
	if c == nil {
		c = new(atomic.Uint64)
		miss[t] = c
	}
	missMu.Unlock()
	c.Add(1)
}

type unmarshaler interface{ UnmarshalSSZ([]byte) error }

// Counters of the whole process (also on the per-type entries).
var stats struct {
	calls, accepted, rejected, runtimeErr atomic.Uint64
	bytesIn, bytesStream                  atomic.Uint64
	ffiNs, buildNs                        atomic.Uint64
	shadowChecked, shadowMismatch         atomic.Uint64
	strictMismatch                        atomic.Uint64
}

func envOn(k string) bool {
	v := os.Getenv(k)
	return v != "" && v != "0" && v != "false"
}

func init() {
	for _, r := range table {
		pt := reflect.TypeOf(r.proto)
		e := &entry{id: r.id, name: r.name, typ: pt.Elem()}
		e.plan = planFor(pt.Elem())
		byType[pt] = e
		order = append(order, e)
	}
	if envOn("BENDSSZ_LOG") || shadow {
		go reporter()
	}
}

func reporter() {
	var last uint64
	for range time.Tick(15 * time.Second) {
		if c := stats.calls.Load(); c != last {
			last = c
			log.Info(Report())
		}
	}
}

// Registered reports whether Unmarshal would handle a message of this type.
func Registered(msg any) bool { _, ok := byType[reflect.TypeOf(msg)]; return ok }

// Unmarshal decodes b into msg (a pointer to one of the registered Prysm
// types) through the Bend library. handled=false means the caller must decode
// with its own UnmarshalSSZ (type not registered, FFI switched off, the Bend
// decoder rejected the bytes - the fastssz decoder then produces the error -
// or the runtime failed). With BENDSSZ_SHADOW=1 every accepted decode is also
// run through the msg's own UnmarshalSSZ into a fresh object and compared; on
// a difference the fastssz result wins and the mismatch is counted.
func Unmarshal(msg any, b []byte) (handled bool, err error) {
	t := reflect.TypeOf(msg)
	e := byType[t]
	if e == nil || off {
		if e == nil && trackMiss {
			missed(t)
		}
		return false, nil
	}
	stats.calls.Add(1)
	stats.bytesIn.Add(uint64(len(b)))

	var t0, t1 time.Time
	if timing {
		t0 = time.Now()
	}
	mu.Lock()
	stream, rc := call(e.id, b)
	if timing {
		t1 = time.Now()
	}
	if rc != 0 || len(stream) < 4 {
		mu.Unlock()
		stats.runtimeErr.Add(1)
		return false, nil
	}
	if stream[0] == 0 { // the status word: 0 rejected, 1 accepted
		mu.Unlock()
		stats.rejected.Add(1)
		e.rej.Add(1)
		if shadow {
			shadowReject(e, msg, b)
		}
		return false, nil
	}
	d := decoder{buf: stream[4:], slab: make([]byte, len(stream)-4)}
	e.plan.run(&d, reflect.ValueOf(msg).UnsafePointer())
	ok := !d.bad && d.pos == len(d.buf)
	stats.bytesStream.Add(uint64(len(stream)))
	mu.Unlock()
	if timing {
		t2 := time.Now()
		stats.ffiNs.Add(uint64(t1.Sub(t0)))
		stats.buildNs.Add(uint64(t2.Sub(t1)))
	}
	if !ok {
		stats.runtimeErr.Add(1)
		log.WithField("type", e.name).Error("C stream does not fit the Go type plan; falling back to fastssz")
		return false, nil
	}
	stats.accepted.Add(1)
	e.n.Add(1)
	if shadow {
		shadowCheck(e, msg, b)
	}
	return true, nil
}

func shadowReject(e *entry, msg any, b []byte) {
	stats.shadowChecked.Add(1)
	ref := reflect.New(e.typ).Interface()
	if ref.(unmarshaler).UnmarshalSSZ(b) == nil {
		mismatch(e, "C rejected bytes that fastssz accepts", nil, nil)
	}
}

var logged sync.Map

func mismatch(e *entry, why string, got, want any) {
	stats.shadowMismatch.Add(1)
	e.bad.Add(1)
	if _, seen := logged.LoadOrStore(e.name+why, true); !seen {
		log.WithField("type", e.name).Errorf("SHADOW MISMATCH: %s", why)
	}
}

func shadowCheck(e *entry, msg any, b []byte) {
	stats.shadowChecked.Add(1)
	ref := reflect.New(e.typ).Interface()
	if err := ref.(unmarshaler).UnmarshalSSZ(b); err != nil {
		mismatch(e, "C accepted bytes that fastssz rejects: "+err.Error(), nil, nil)
		return
	}
	pm, ok1 := msg.(proto.Message)
	pr, ok2 := ref.(proto.Message)
	if !ok1 || !ok2 {
		return
	}
	if !proto.Equal(pm, pr) {
		mismatch(e, "decoded objects differ (proto.Equal)", nil, nil)
		reflect.ValueOf(msg).Elem().Set(reflect.ValueOf(ref).Elem())
		return
	}
	if strict && !reflect.DeepEqual(msg, ref) {
		stats.strictMismatch.Add(1)
		if _, seen := logged.LoadOrStore(e.name+"strict", true); !seen {
			log.WithField("type", e.name).Warn("decoded objects are proto.Equal but not reflect.DeepEqual (nil vs empty slice?)")
		}
	}
}

// Report is a one-line summary of the FFI decode counters.
func Report() string {
	var per []string
	for _, e := range order {
		if n := e.n.Load(); n > 0 || e.rej.Load() > 0 {
			s := fmt.Sprintf("%s=%d", e.name, n)
			if r := e.rej.Load(); r > 0 {
				s += fmt.Sprintf("(rej %d)", r)
			}
			per = append(per, s)
		}
	}
	sort.Strings(per)
	var ms []string
	missMu.Lock()
	for t, c := range miss {
		name := t.String()
		if t.Kind() == reflect.Ptr {
			name = t.Elem().Name()
		}
		ms = append(ms, fmt.Sprintf("%s=%d", name, c.Load()))
	}
	missMu.Unlock()
	sort.Strings(ms)
	return fmt.Sprintf("decoded through C: %d (rejected %d, runtime errors %d); shadow fastssz checks %d, mismatches %d (strict %d); in %d B; ffi %.1f ms, go build %.1f ms; [%s]; fastssz only (type not covered): [%s]",
		stats.accepted.Load(), stats.rejected.Load(), stats.runtimeErr.Load(),
		stats.shadowChecked.Load(), stats.shadowMismatch.Load(), stats.strictMismatch.Load(),
		stats.bytesIn.Load(), float64(stats.ffiNs.Load())/1e6, float64(stats.buildNs.Load())/1e6,
		strings.Join(per, " "), strings.Join(ms, " "))
}
