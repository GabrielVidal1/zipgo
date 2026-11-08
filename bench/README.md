# zipgo bench — how many visitors can this box take?

A load test for a zipgo host, measured the way visitors arrive: it steps the
load up with [k6](https://k6.io) until the server stops keeping up, and says
for each kind of traffic where it broke and what ran out (CPU, network, a
container, PocketBase).

Nothing to install but Docker (k6 runs from the `grafana/k6` image) and
Python 3 for the report. The server needs SSH for the samples.

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
| `handshake` | one request on a brand-new TLS connection | TLS handshakes (and a proxy in front) |
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
ladder (smoke tests), `--step 60s` lengthens the steps.

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
