// C ABI of libbendssz (bend-ssz compiled to C by Bend; see bendssz/ssz_ffi.c).
#ifndef BENDSSZ_H
#define BENDSSZ_H
#include <stdint.h>

// Brings the Bend runtime up; implied by the first bendssz_call. 0 on success.
int bendssz_init(void);

// Decode the SSZ bytes in[0..n) as type `ty` (ids: bendssz/types.txt).
// On return 0, *out/*out_len describe a library-owned byte stream, valid until
// the next call: [1, structural dump of the decoded object ...] if the bytes
// are a valid SSZ encoding of the type, [0] if they are not.
// Not reentrant and not thread safe: the caller serialises calls.
int bendssz_call(uint32_t ty, const uint8_t* in, uint32_t n,
                 const uint8_t** out, uint64_t* out_len);

// Words of the Bend heap taken so far (a leak detector for tests).
uint64_t bendssz_heap_words(void);
#endif
