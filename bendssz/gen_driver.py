#!/usr/bin/env python3
"""Write bend/entry.bend for the names in types.txt: the request loop (Ssz.exchange)
and a dispatcher  ty -> X_decode + X_dump.  The response stream is
    [1, flat words...]   the SSZ bytes were accepted; X_flat: the decoded object as 32-bit words
    [0]                  rejected (X_decode returned None)
usage: gen_driver.py types.txt out.bend [import-prefix]"""
import sys

rows = []
for line in open(sys.argv[1]):
    line = line.split('#')[0].strip()
    if line:
        i, name, go = line.split()
        rows.append((int(i), name, go))
out = sys.argv[2]
pre = sys.argv[3] if len(sys.argv) > 3 else '..'
L = ['import Base', f'import {pre}/src/buffer.bend as B']
for _, n, _ in rows:
    L.append(f'import {pre}/types/Fulu{n}_def_generated.bend as {n}_d')
    L.append(f'import {pre}/types/Fulu{n}_decode_ssz_generated.bend as {n}_r')
L += ['',
      '# Foreign effects: implemented in ssz_ffi.c (the C ABI of the library).',
      '# Ssz.exchange(response): hand the previous response (Nil the first time) to C, then',
      '# receive the next request (ty, (size, words)); parks until the caller posts one.',
      'def Ssz.exchange(+xs: +List<U32>) -> IO(U32 & (U32 & Array<U32>)):',
      '  import "./ssz_ffi.c"', '']
for _, n, _ in rows:
    L += [f'def {n}_some(m: Maybe<&1, {n}_d.{n}>) -> +List<U32>:',
          '  match m:',
          '    case None{}: 0 <> []',
          f'    case Some{{o}}: 1 <> {n}_d.{n}_flat(o, [])', '',
          f'def {n}_run(pair: B.Buf & Maybe<&1, {n}_d.{n}>) -> +List<U32>:',
          '  (b, m) = pair',
          f'  {n}_some(m)', '']
# dispatcher: one literal-case match on the type id. (A chain of two-armed matches costs
# ~20 ns per id; the compiler turns the literal match into a decision tree.) The default arm,
# one id past the last type, is a no-op round trip (status word only): it measures the cost of
# the boundary alone (cgo call + Bend request loop), nothing decoded.
L += ['def dispatch(+ty: U32, buf: B.Buf, +n: U32) -> +List<U32>:', '  match ty:']
for k, (i, n, _) in enumerate(rows):
    assert i == k, 'ids must be 0..n-1 in order'
    L.append(f'    case {k}: {n}_run({n}_r.{n}_decode(buf, n))')
L += ['    case _: 0 <> []', '']
L += ['def handle(req: U32 & (U32 & Array<U32>)) -> +List<U32>:',
      '  (+ty, rest) = req',
      '  (+n, a) = rest',
      '  dispatch(ty, B.Buf{a, n}, n)', '',
      '@unsafe',
      'def serve(+xs: +List<U32>) -> IO(Unit):',
      '  do IO<Unit>:',
      '    req : U32 & (U32 & Array<U32>) <- Ssz.exchange(xs)',
      '    serve(handle(req))', '',
      'def main() -> IO(Unit): serve([])', '']
open(out, 'w').write('\n'.join(L))
print('wrote', out, len(rows), 'types')
