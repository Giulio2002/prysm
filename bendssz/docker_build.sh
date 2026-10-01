#!/bin/sh
# Build the two local images (beacon-chain with the bendssz tag, validator) on the build server.
#   docker_build.sh <prysm checkout with encoding/bendssz/lib/bendssz.c> <GOPATH> <GOCACHE> <tag>
# The Go compile runs in golang:1.26-bookworm (so the binary links against bookworm's glibc, like the
# upstream runtime image) with the host's module and build caches mounted; libbendssz.a is compiled
# in the container from the emitted bendssz.c. The runtime stage is debian:bookworm-slim (ethereum-package starts the CL with `sh -c`, so a shell is needed).
set -eu
SRC=$1; GP=$2; GC=$3; TAG=${4:-bendssz-dev}
OUT=$SRC/../dockerout; mkdir -p "$OUT"
# go does not track the contents of libbendssz.a (a cgo LDFLAGS input): a changed library with
# unchanged Go sources would be served from the link cache. The library's hash goes into the
# version string, which is a link input, so a new library always relinks.
LIBSUM=$(sha256sum "$SRC/encoding/bendssz/lib/bendssz.c" | cut -c1-12)
docker run --rm --memory 24g --cpus 8 \
  -v "$SRC":/src -v "$GP":/go -v "$GC":/root/.cache/go-build -v "$OUT":/out -w /src \
  -e CGO_ENABLED=1 -e CGO_CFLAGS="-D__BLST_PORTABLE__" -e GOFLAGS=-buildvcs=false -e GOTOOLCHAIN=local -e LIBSUM=$LIBSUM \
  golang:1.26-bookworm sh -c '
    set -e
    apt-get update -qq && apt-get install -y -qq clang >/dev/null
    clang -std=c11 -O3 -c -fPIC -Dmain=bendssz_unused_main encoding/bendssz/lib/bendssz.c -o /tmp/bendssz.o
    rm -f encoding/bendssz/lib/libbendssz.a && ar rcs encoding/bendssz/lib/libbendssz.a /tmp/bendssz.o
    nice -n 10 go build -p 8 -tags bendssz -ldflags "-X github.com/OffchainLabs/prysm/v7/runtime/version.gitTag=bendssz-$LIBSUM" -o /out/beacon-chain ./cmd/beacon-chain
    nice -n 10 go build -p 8 -ldflags "-X github.com/OffchainLabs/prysm/v7/runtime/version.gitTag=bendssz" -o /out/validator ./cmd/validator
    rm -f encoding/bendssz/lib/libbendssz.a'
for BIN in beacon-chain validator; do
  cat > "$OUT/Dockerfile.$BIN" <<EOD
FROM debian:bookworm-slim
RUN apt-get update -qq && apt-get install -y -qq ca-certificates curl >/dev/null && rm -rf /var/lib/apt/lists/*
COPY $BIN /$BIN
ENTRYPOINT ["/$BIN"]
EOD
  docker build -q -f "$OUT/Dockerfile.$BIN" -t "prysm-$BIN:$TAG" "$OUT"
done
docker images | grep "^prysm-"
