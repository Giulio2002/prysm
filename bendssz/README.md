# bendssz: SSZ decode through the formally checked bend-ssz library

Prysm decodes SSZ with generated fastssz-style `UnmarshalSSZ` methods. Built with the Go tag
`bendssz`, the decode of a set of types instead goes through
[bend-ssz](https://github.com/Giulio2002/bend-ssz) (SSZ written and proved in Bend, compiled to C):

    SSZ bytes --cgo--> libbendssz.a (C): the Bend runtime validates the bytes, decodes them into its
                       own heap object, flattens the object into a flat C stream (the "C object")
    C stream --Go----> the Prysm (protobuf) struct, built by a plan compiled once per type from the
                       struct tags (encoding/bendssz/plan.go). Only the types are built on the Go side.

`hash_tree_root` and serialisation stay on the fastssz side. Without the tag nothing changes
(`encoding/bendssz/off.go` is a stub and every caller falls back to `UnmarshalSSZ`).

## Files

| file | what |
|---|---|
| `encoding/bendssz/` | the Go side: cgo binding (`ffi.go`), plan builder (`plan.go`), `Unmarshal` + shadow mode + counters (`api.go`), registry (`registry.go`, generated), tests |
| `bendssz/ssz_ffi.c` | the C ABI, spliced into the Bend-emitted C as a foreign effect (see below) |
| `bendssz/gen_driver.py`, `types.txt` | writes the Bend driver (request loop + dispatcher) for the types in `types.txt` |
| `bendssz/flat-generate.patch` | adds the word-granular `X_flat` twin of bend-ssz's `X_dump` (applied to a scratch copy of bend-ssz; the checkout is not modified) |
| `bendssz/build_lib.sh` | bend-ssz checkout -> `bendssz.c` -> `libbendssz.a` (needs a stock Bend 2.0.34 and clang; heavy, server only) |
| `bendssz/docker_build.sh`, `kurtosis-args.yaml`, `collect_evidence.sh` | local images, the Kurtosis devnet (2 Prysm with the tag + 1 Lighthouse, mainnet preset, Fulu, spamoor transactions and blobs) and a snapshot of its head/finality/FFI counters |
| `bendssz/test_fx.c`, `test_ids.c` | C harnesses of the library (fixtures through the ABI; ns per round trip per type id) |

bend-ssz is private and its emitted C derives from it, so neither is committed here
(`encoding/bendssz/lib/` is git-ignored): build the library yourself.

## The C ABI (`encoding/bendssz/bendssz.h`)

    int bendssz_call(uint32_t ty, const uint8_t* in, uint32_t n, const uint8_t** out, uint64_t* out_len);

A Bend def is not a symbol of the emitted C (the compiler inlines everything into `main`), so the
library uses Bend's foreign effects: the Bend `main` is a request loop
`req <- Ssz.exchange(response)`, with `Ssz.exchange` written in C (`ssz_ffi.c`). `bendssz_call`
stores the request, resumes the parked `Ssz.exchange` activation and steps the Bend IO loop on the
caller's thread until it parks again; no helper thread, futex or pipe. The response is a stream of
little-endian 32-bit words: `[1, X_flat(object) ...]` or `[0]` (not a valid encoding). Calls are
serialised by a Go mutex (one Bend heap).

## Build and run

    bendssz/build_lib.sh <bend-ssz> <bend 2.0.34> <out>      # -> <out>/bendssz.{c,o}, libbendssz.a
    cp <out>/libbendssz.a encoding/bendssz/lib/
    go build -tags bendssz ./cmd/beacon-chain
    # tests (need BENDSSZ_FIXTURES=<dir>/<Name>/ssz_random/*.ssz, the decompressed consensus-spec-tests)
    go test -tags bendssz ./encoding/bendssz -run 'Fixtures|Mutations|RandomObjects|Soak|Concurrent'   # add -race for the last
    go test -tags bendssz ./encoding/bendssz -bench 'Decode|Boundary|Parallel'

Go does not track the contents of `libbendssz.a`: touch `encoding/bendssz/ffi.go` after replacing it.

Runtime switches (environment): `BENDSSZ_SHADOW=1` decodes every message again with fastssz and
compares (fastssz wins and the mismatch is counted if they differ), `BENDSSZ_STRICT=1` also compares
with `reflect.DeepEqual`, `BENDSSZ_LOG=1` logs the counters every 15 s, `BENDSSZ_TIMING=1` times the
C call and the Go build separately, `BENDSSZ_OFF=1` disables the FFI path.

## Hooks

`bendssz.UnmarshalSSZ(msg, bytes)` replaces `msg.UnmarshalSSZ(bytes)` in: the p2p SSZ encoder
(`beacon-chain/p2p/encoder/ssz.go`), the beacon DB (`db/kv/encoding.go`, Fulu block and state),
`encoding/ssz/detect` (genesis / checkpoint state, blocks) and `consensus-types/blocks`
(`SignedBeaconBlock.UnmarshalSSZ`, Fulu).
