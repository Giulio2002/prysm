// bendssz C ABI: spliced into the program's C source by the Bend compiler
// (the `import "./ssz_ffi.c"` of the foreign effect Ssz.exchange),
// after the Bend runtime, so every runtime symbol is in scope.
//
// The Bend program is a request loop:   req <- Ssz.exchange(response); response = handle(req); loop
// The library runs that loop cooperatively on the CALLER's thread: a request is
// stored, the parked Ssz.exchange activation is made runnable, and the Bend IO loop
// is stepped until it parks on the next Ssz.exchange again. No helper thread, no
// futex, no pipe is involved in a call. Calls are serialised by the caller
// (the Go side holds a mutex).

#define BENDSSZ_API __attribute__((visibility("default")))

static IoWork* ssz_wait;          // the activation parked in Ssz.exchange, or NULL
static int     ssz_up;            // runtime initialised
static int     ssz_loop_up;       // main spawned
static u32     ssz_in_ty, ssz_in_n;
static const uint8_t* ssz_in_p;
static int     ssz_have;          // a request is pending
static uint8_t* ssz_out;          // response bytes, malloc'd, reused
static u64     ssz_out_cap, ssz_out_len;
static int     ssz_sent;          // a response was produced by Ssz.exchange
static u64     ssz_calls, ssz_cells;

// The request as Bend sees it: (ty, (n, words)); words is a fresh zeroed
// Array<U32> of 2^d words holding the n input bytes (little-endian packed).
static Term ssz_make(Env e) {
  u32 words = (ssz_in_n + 3) / 4;
  u32 d = 0;
  while ((1u << d) < words) {
    d += 1;
  }
  Term zero = 0;
  Term arr  = blk_new(e, 0, d, 0, 1, &zero);
  u32* dst  = (u32*)blk_ptr(e.mem, blk_loc(e.mem, arr), 0);
  memcpy(dst, ssz_in_p, ssz_in_n);
  return io_tup(e, (Term)ssz_in_ty, io_tup(e, (Term)ssz_in_n, arr));
}

// Ssz.exchange(xs): xs is the previous response (Nil on the first round): its
// items, each a 32-bit word, go into ssz_out as little-endian bytes. Then the
// effect answers the next request, or parks the activation until the caller
// posts one.
Term ssz_exchange_run(Env e, Term* f, IoWork* w) {
  Term xs = f[0];
  if (term_aux(xs) == CID_CON) {
    u64 n = 0;
    while (term_aux(xs) == CID_CON) {
      Term fb[2];
      spare_free(e, cls_fit(2), ctr_take(e, xs, 2, fb));
      if ((n + 1) * 4 > ssz_out_cap) {
        ssz_out_cap = ssz_out_cap ? ssz_out_cap * 2 : 1 << 16;
        ssz_out = io_mem(realloc(ssz_out, ssz_out_cap));
      }
      u32 v = (u32)fb[0];
      memcpy(ssz_out + n * 4, &v, 4);
      n += 1;
      xs = fb[1];
    }
    ssz_out_len = n * 4;
    ssz_cells  += n;
    ssz_sent = 1;
  }
  if (!ssz_have) {
    ssz_wait = w;
    return IO_PARK;
  }
  ssz_have = 0;
  return ssz_make(e);
}

static void __attribute__((constructor)) ssz_ffi_use(void) {
  io_eff(CID(Ssz.exchange), ssz_exchange_run, 0);
}

// The signal-free twin of pool_stack(): Go owns SIGSEGV and friends.
static Term* ssz_stack(void) {
  u64   len = 1ull << 31;
  char* p   = pool_mmap(len + 16384);
  if (mprotect(p + len, 16384, PROT_NONE) != 0) {
    err_fail("stack guard failed");
  }
  return (Term*)p;
}

BENDSSZ_API int bendssz_init(void) {
  if (ssz_up) {
    return 0;
  }
  corpus_setup(false, 1, 0);
  io_stk = ssz_stack();
  ssz_up = 1;
  return 0;
}

// One request. ty selects the SSZ type (the Bend dispatcher's numbering). The
// response is a stream of little-endian 32-bit words in a library-owned buffer
// (out_len bytes), valid until the next call: [1, the decoded object flattened
// by X_flat ...], or [0] if the bytes are not a valid SSZ encoding of the type. Returns 0, or < 0 on a runtime error.
BENDSSZ_API int bendssz_call(uint32_t ty, const uint8_t* in, uint32_t n,
                             const uint8_t** out, uint64_t* out_len) {
  if (!ssz_up && bendssz_init() != 0) {
    return -1;
  }
  Env e = { CORPUS, ALC[0] };
  ssz_in_ty = ty;
  ssz_in_n  = n;
  ssz_in_p  = in;
  ssz_sent  = 0;
  ssz_calls += 1;
  if (!ssz_loop_up) {
    ssz_have = 1;
    Term m = corpus_eval(CORPUS, term_tsk(MAIN_FID, task_node(e, MAIN_FID, TERM_HOLE, 0, 0)));
    io_spawn(m);
    ssz_loop_up = 1;
  } else {
    IoWork* a = ssz_wait;
    if (a == NULL) {
      return -2;
    }
    ssz_wait = NULL;
    a->item  = ssz_make(e);
    io_push(&io_runs, a);
  }
  while (io_runs != NULL) {
    io_step(e, io_pop(&io_runs));
  }
  if (!ssz_sent) {
    return -4;
  }
  ssz_in_p = NULL;
  *out     = ssz_out;
  *out_len = ssz_out_len;
  return 0;
}

BENDSSZ_API uint64_t bendssz_heap_words(void) {
  return ssz_up ? (uint64_t)a32_load(a32_at(CORPUS, H_BUMP)) : 0;
}
