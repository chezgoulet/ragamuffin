# Heap profiling during a reindex (#880)

Ragamuffin has no profiling endpoints unless you opt in. The full-reindex
memory growth reported in #880 is not yet fixed; this page describes the
instrument used to diagnose it.

## Enabling the listener

Set one environment variable and restart the process:

```bash
RAGAMUFFIN_PPROF_ADDR=127.0.0.1:6060
```

| Value | Behaviour |
|---|---|
| unset / empty | pprof is disabled (default) |
| `127.0.0.1:6060` | enabled on the IPv4 loopback interface |
| `[::1]:6060` | enabled on the IPv6 loopback interface |
| `localhost:6060` | enabled on loopback |
| `0.0.0.0:6060`, `:6060`, a LAN IP | **rejected at startup** |

The check runs twice: in `Config.Validate()` (fail fast with a config error)
and again in `server.NewPprofServer` (a programmatic caller cannot bypass it).
The handlers are served by a **separate `http.Server`**; they are never mounted
on the public API mux, so `RAGAMUFFIN_PORT` cannot expose them. There is no
authentication because the listener cannot be reached off-host: the bind
address is the boundary.

If you need remote access, use an SSH tunnel instead of widening the bind:

```bash
ssh -L 6060:127.0.0.1:6060 user@host
```

## Taking a heap profile during a reindex

Start the reindex as usual, then, once RSS is climbing:

```bash
# In-process listener (bare metal / systemd):
curl -sS -o /tmp/reindex-heap.pprof \
  'http://127.0.0.1:6060/debug/pprof/heap?seconds=30'

# Docker: the listener is inside the container's loopback namespace.
docker exec ragamuffin wget -q -O /tmp/reindex-heap.pprof \
  'http://127.0.0.1:6060/debug/pprof/heap?seconds=30'
docker cp ragamuffin:/tmp/reindex-heap.pprof /tmp/reindex-heap.pprof
```

`?seconds=30` samples allocations for 30 seconds. For a leak that grows over
minutes, that is more useful than the instant snapshot.

Take a second profile before the reindex starts, so the two can be diffed:

```bash
curl -sS -o /tmp/before-heap.pprof 'http://127.0.0.1:6060/debug/pprof/heap'
```

## Reading the profile

```bash
go tool pprof -top -nodecount=40 /tmp/reindex-heap.pprof
go tool pprof -sample_index=alloc_space -top /tmp/reindex-heap.pprof
go tool pprof -http=127.0.0.1:8081 /tmp/reindex-heap.pprof
```

`inuse_space` shows what is live right now; `alloc_space` shows cumulative
allocation, which is the better signal when the question is "what is growing
per file". Diff a pre-reindex and mid-reindex profile with
`go tool pprof -base /tmp/before-heap.pprof /tmp/reindex-heap.pprof`.

Other endpoints on the same listener: `/debug/pprof/goroutine?debug=2`,
`/debug/pprof/allocs`, `/debug/pprof/profile?seconds=30` (CPU),
`/debug/pprof/trace?seconds=5`.

## What this does and does not do

- It is an instrument, not a fix. #880 remains open until the reindex path is
  bounded and verified with these profiles.
- It adds no dependency: `net/http/pprof` is in the standard library and is
  only linked in when the listener starts.
- Leave `RAGAMUFFIN_PPROF_ADDR` unset in normal operation. A heap profile
  contains live process memory, including credentials held in memory.
