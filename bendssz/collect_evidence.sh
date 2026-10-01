#!/bin/sh
# Snapshot of the Kurtosis enclave: head slot, finality, peers, FFI counters, anything suspicious.
E=${1:-bendssz-prysm}
echo "== $(date -u +%FT%TZ)  enclave $E"
for n in cl-1-prysm-geth cl-2-prysm-geth cl-3-lighthouse-geth; do
  P=$(kurtosis port print $E $n http 2>/dev/null) || continue
  echo "--- $n  ($P)"
  echo "head:       $(curl -s $P/eth/v1/beacon/headers/head | grep -o '"slot":"[0-9]*"' | head -1)"
  echo "finality:   $(curl -s $P/eth/v1/beacon/states/head/finality_checkpoints | grep -o '"current_justified":{[^}]*}\|"finalized":{[^}]*}' | tr '\n' ' ')"
  echo "peers:      $(curl -s $P/eth/v1/node/peer_count | head -c 200)"
  echo "version:    $(curl -s $P/eth/v1/node/version | head -c 120)"
done
for n in cl-1-prysm-geth cl-2-prysm-geth; do
  echo "--- $n log"
  kurtosis service logs $E $n 2>&1 > /tmp/prysm_ffi_$n.log
  echo "last bendssz counter line:"; grep 'bendssz:' /tmp/prysm_ffi_$n.log | tail -1 | sed 's/\x1b\[[0-9;]*m//g'
  echo "first bendssz counter line:"; grep 'bendssz:' /tmp/prysm_ffi_$n.log | head -1 | sed 's/\x1b\[[0-9;]*m//g'
  echo "SHADOW MISMATCH lines: $(grep -c 'SHADOW MISMATCH' /tmp/prysm_ffi_$n.log)   panic/fatal lines: $(grep -ci 'panic\|fatal' /tmp/prysm_ffi_$n.log)   'does not fit the Go type plan': $(grep -c 'does not fit' /tmp/prysm_ffi_$n.log)"
  echo "synced blocks logged: $(grep -c 'Synced new block' /tmp/prysm_ffi_$n.log)"
done
