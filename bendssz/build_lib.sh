#!/bin/sh
# Build libbendssz.a (+ bendssz.c) from a bend-ssz checkout. Heavy: run on the build server only.
#   build_lib.sh <bend-ssz checkout> <stock bend binary> <out dir> [python3]
# The checkout is never modified: a scratch copy gets bendssz/flat-generate.patch (the word-granular
# X_flat twin of X_dump), is regenerated, and the driver is compiled from that copy.
set -eu
SSZ=$1; BEND=$2; OUT=$3; PY=${4:-python3}
HERE=$(cd "$(dirname "$0")" && pwd)
W=$OUT/ssz
mkdir -p "$OUT" "$W"
for d in src types codegen schemas spec tools vendor; do rsync -a --delete --exclude __pycache__ "$SSZ/$d/" "$W/$d/"; done
for f in toolchain.lock.json cases.json; do cp "$SSZ/$f" "$W/" 2>/dev/null || true; done
( cd "$W" && patch -s -p1 < "$HERE/flat-generate.patch" && nice -n 10 "$PY" codegen/generate.py )
"$PY" "$HERE/gen_driver.py" "$HERE/types.txt" "$W/ffi_entry.bend" .
cp "$HERE/ssz_ffi.c" "$W/ssz_ffi.c"
cd "$W"
nice -n 10 env BEND_NO_TELEMETRY=1 BUN_JSC_forceRAMSize=3000000000 "$BEND" ffi_entry.bend -o "$OUT/bendssz.c"
nice -n 10 clang -std=c11 -O3 -c -fPIC -Dmain=bendssz_unused_main "$OUT/bendssz.c" -o "$OUT/bendssz.o"
rm -f "$OUT/libbendssz.a"
ar rcs "$OUT/libbendssz.a" "$OUT/bendssz.o"
ls -la "$OUT"
