// C harness: ns per round trip for each type id with a fixed small input (id 36 = no-op).
#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
int bendssz_call(uint32_t ty, const uint8_t* in, uint32_t n, const uint8_t** out, uint64_t* out_len);
static double now(void){struct timespec t;clock_gettime(CLOCK_MONOTONIC,&t);return t.tv_sec*1e9+t.tv_nsec;}
int main(int argc, char** argv) {
  uint8_t in[128]; memset(in, 7, sizeof in);
  const uint8_t* out; uint64_t len;
  int ids[] = {0, 1, 2, 4, 9, 17, 27, 35, 36};
  for (int k = 0; k < 9; k++) {
    int N = 200000;
    double t0 = now();
    for (int i = 0; i < N; i++) bendssz_call(ids[k], in, ids[k] == 1 ? 40 : 128, &out, &len);
    printf("id %d: %.0f ns/call (resp %llu bytes)\n", ids[k], (now() - t0) / N, (unsigned long long)len);
  }
  return 0;
}
