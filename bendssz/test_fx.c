// C harness: run every fixture through the library; print status / stream size / time.
#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <dirent.h>
int bendssz_call(uint32_t ty, const uint8_t* in, uint32_t n, const uint8_t** out, uint64_t* out_len);
uint64_t bendssz_heap_words(void);
static double now(void){struct timespec t;clock_gettime(CLOCK_MONOTONIC,&t);return t.tv_sec*1e3+t.tv_nsec/1e6;}
int main(int argc, char** argv) {
  // argv: id dir  -> every .ssz in dir
  uint32_t id = atoi(argv[1]);
  DIR* d = opendir(argv[2]); struct dirent* de;
  while ((de = readdir(d))) {
    if (!strstr(de->d_name, ".ssz")) continue;
    char p[1024]; snprintf(p, sizeof p, "%s/%s", argv[2], de->d_name);
    FILE* f = fopen(p, "rb"); fseek(f, 0, SEEK_END); long n = ftell(f); fseek(f, 0, SEEK_SET);
    uint8_t* b = malloc(n); fread(b, 1, n, f); fclose(f);
    const uint8_t* out; uint64_t len;
    double t0 = now(); int r = bendssz_call(id, b, n, &out, &len); double t1 = now();
    printf("%s in=%ld r=%d status=%u out_words=%llu ms=%.3f heap=%llu\n", de->d_name, n, r, r==0?((uint32_t*)out)[0]:99, (unsigned long long)len/4, t1-t0, (unsigned long long)bendssz_heap_words());
    free(b);
  }
  return 0;
}
