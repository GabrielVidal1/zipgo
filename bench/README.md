# zipgo bench — how many visitors can this box take?

A load test for a zipgo host, measured the way visitors arrive: it steps the
load up with [k6](https://k6.io) until the server stops keeping up, and says
for each kind of traffic where it broke and what ran out (CPU, network, a
container, PocketBase).

Nothing to install but Docker (k6 runs from the `grafana/k6` image; the
new-connection client, `bench/connload`, is built with the local Go or in the
`golang` image on first use) and Python 3 for the report. The server needs
SSH for the samples.

## 1. A target that isn't a real site

```bash
bench/setup.sh --host bench.example.com --target user@server:/srv/domains \
    --build path/to/a/real/dist --pb ~/services/bench-pocketbase
```

- `--build` deploys a copy of a real build (pick a heavy one) at
  `https://bench.example.com/`.
- `--pb` starts a throwaway PocketBase (`bench/pocketbase/`: 1,000 rows of
  filler, public reads, writes only with a secret header) on the server's
  Docker network and points `https://pb.bench.example.com/` at it. It prints
  the `BENCH_KEY` the write scenario needs.

`bench/teardown.sh` (same `--host`/`--target`/`--pb`) removes all of it.

## 2. Run

```bash
bench/run.sh --site https://bench.example.com/ --api https://pb.bench.example.com \
    --resolve 192.168.1.131 --ssh user@server --bench-key KEY --label baseline
```

`--resolve` sends every request to that address (a LAN test of the box
itself, without the uplink in the way). Leave it out to go through DNS.

| scenario | one arrival is | stresses |
|---|---|---|
| `static` | one request for a small file, connections kept alive | routing + file serving, raw req/s |
| `firstvisit` | a new visitor: the page + every asset it links, on a new connection | bytes: NIC, uplink, compression |
| `handshake` | one request on a brand-new TLS connection (connload, not k6) | TLS handshakes (and a proxy in front) |
| `api-read` | a PocketBase list request through zipgo | reverse proxy + PocketBase + SQLite |
| `api-write` | a PocketBase create | SQLite's single writer |
| `mixed` | a visitor: the page, then 3 API reads a second apart | the headline "visitors per second" |

Each scenario climbs a ladder of arrival rates (`run.sh` → `ladder`), 30 s a
step, and stops at the first step with more than 1 % failed requests, more
than 2 % of arrivals k6 couldn't start (all its VUs stuck on a slow server),
a p95 over budget (2 s for a whole page, 1 s for a request) or a NIC TX
timeout on the server. Each step also samples the server over SSH: CPU (busy
and softirq — packet processing), NIC throughput, and `docker stats` mid-step.
Server throughput is averaged over the k6 container's whole run, so it reads
a few percent low.

`--scenarios static,firstvisit` picks some, `--rates "5 10"` replaces every
ladder (smoke tests), `--step 60s` lengthens the steps, `--verify` makes the
clients check the server's certificate (off by default — see below).

### Don't measure the client

A load generator runs out too, and when it does its limit reads like the
server's. Two rules came out of that (raspy2, 2026-11-09: a "340 new
connections/s at 40 % CPU" ceiling that was really ~1,400/s at 99 %):

- **Certificates aren't verified by default.** Checking the chain is client
  work the server never sees, and it is expensive: Let's Encrypt's
  intermediates carry P-384 keys, which Go (k6, connload) verifies in generic
  code — ~14 ms of CPU per connection on a 4-core laptop CPU, so a few hundred
  new connections a second fill the client. Real visitors verify on their own
  devices. `--verify` turns it back on (to check a certificate, not capacity).
- **New connections come from connload** (`bench/connload`, a few hundred
  lines of Go), not k6: even without verification k6 tops out around 770 new
  TLS connections/s on that laptop, connload passes 2,000/s at ~80 % of it.
  `saturate.sh --new-conn --k6` still runs k6, for comparison.

Every step also samples **this machine's CPU**. The console line and the
report show it (`client 71%`), and past 80 % a step is marked
**⚠ client-bound**: read those numbers as a floor, not the server's limit.

connload also runs on its own, with two switches a server can't tell from
real visitors:

```bash
go run ./bench/connload -url https://bench.example.com/vite.svg -resolve 192.168.1.131 -c 256 -d 20s
#   -resume   resume TLS sessions, like a returning visitor (raspy2: +13 %)
#   -x25519   offer X25519 only, not the post-quantum hybrid browsers send (+15 %)
#   -rate N   open loop: N arrivals/s instead of a closed loop of -c connections
```

Run `api-write` **last**, and start each run on a fresh PocketBase: every
write adds a row, and PocketBase counts the whole collection on every list
request, so a run after 50,000 writes measures a bigger database, not a
faster or slower server (`setup.sh --pb` on an empty folder seeds 1,000 rows).

### The ceiling

The ladder measures what a rate of *arrivals* costs, the way visitors come.
Its first step opens a burst of connections at once, so a step can fail on a
few slow connects while the server is far from busy — read the CPU column.
For the most the server can answer, `bench/saturate.sh` runs N clients in a
closed loop (each sends its next request as soon as the last is answered) on
one small file, with connections kept alive or `--new-conn` (a TLS handshake
per request, from connload), and adds a *Ceiling* table to that run's
report:

```bash
bench/saturate.sh --into bench/results/<run> --url https://bench.example.com/vite.svg \
    --resolve 192.168.1.131 --ssh user@server --vus "16 64 256" [--new-conn]
```

To see what a proxy in front costs, run the same `--new-conn` ceiling
through it and straight to the server (a second container on a published
port): on raspy2, Traefik's TCP passthrough with PROXY v2 took a third of the
CPU per new connection (1,400/s through it, 2,100/s without).

## 3. Read

`bench/results/<label>-<stamp>/report.md`: the sustained rate per scenario
and why the next step failed, then every step. With several runs it compares
them (the first is the reference):

```bash
python3 bench/report.py report bench/results/baseline-* bench/results/after-*
```

The mixed scenario's rate turns into "people on the site at once" with
Little's law: arrivals per second × how long a visit lasts (the report
assumes 3 minutes).

### PocketBase's request log

PocketBase stores a log row for every request, so every API read is also a
database write (batched, up to 200 rows a transaction). It logs successes at
INFO and failures at ERROR, so `bench/pocketbase/logs.sh … off` raises the
bench instance's minimum level to WARN — no more row per successful request,
errors still kept — and `on` puts the defaults back, to measure what it costs:

```bash
bench/pocketbase/logs.sh --api https://pb.bench.example.com --server user@server \
    --resolve 192.168.1.131 off
```
