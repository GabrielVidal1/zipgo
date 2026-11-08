#!/usr/bin/env python3
"""zipgo bench — turn k6 summaries and server samples into steps and reports.

  report.py step   --dir STEP_DIR --scenario S --rate R --seconds T --budget MS
                   → one JSON line for that step (bench/run.sh appends it)
  report.py line   --json LINE      → the one-line console summary of a step
  report.py report RUN_DIR [RUN_DIR…] → Markdown: every step of each run, the
                   capacity of each scenario, and a comparison when several
                   runs are given (the first is the reference)

Standard library only.
"""
import argparse
import json
import os
import sys

SESSION_MINUTES = 3  # how long a visitor stays, to turn visitors/s into people at once


def _num(x, default=0.0):
    try:
        return float(x)
    except (TypeError, ValueError):
        return default


def k6_summary(path):
    with open(path) as f:
        m = json.load(f)["metrics"]

    def v(name, key, default=0.0):
        return _num(m.get(name, {}).get("values", {}).get(key), default)

    return {
        "iterations": v("iterations", "count"),
        "iter_rate": v("iterations", "rate"),
        "dropped": v("dropped_iterations", "count"),
        "req_rate": v("http_reqs", "rate"),
        "reqs": v("http_reqs", "count"),
        "failed": v("http_req_failed", "rate"),
        "req_p50": v("http_req_duration", "med"),
        "req_p95": v("http_req_duration", "p(95)"),
        "req_p99": v("http_req_duration", "p(99)"),
        "iter_p50": v("iteration_duration", "med"),
        "iter_p95": v("iteration_duration", "p(95)"),
        "recv_rate": v("data_received", "rate"),
        "checks": v("checks", "rate", 1.0),
        "vus_max": v("vus_max", "value"),
    }


def read_probe(path):
    """cpu jiffies, NIC rx/tx bytes and the TX-timeout total from probe()."""
    try:
        lines = [l for l in open(path).read().splitlines() if l.strip()]
    except OSError:
        return None
    out = {}
    for l in lines:
        if l.startswith("cpu "):
            f = [int(x) for x in l.split()[1:]]
            f += [0] * (10 - len(f))
            out["cpu"] = f
        elif ":" in l:
            f = l.split(":", 1)[1].split()
            out["rx"], out["tx"] = int(f[0]), int(f[8])
        elif l.strip().isdigit():
            out["tx_timeout"] = int(l.strip())
    return out if "cpu" in out else None


def server_delta(before, after, seconds):
    if not before or not after:
        return {}
    d = [a - b for a, b in zip(after["cpu"], before["cpu"])]
    total = sum(d[:8]) or 1
    user, nice, system, idle, iowait, irq, softirq, steal = d[:8]
    out = {
        "cpu_busy": 100.0 * (total - idle - iowait) / total,
        "cpu_softirq": 100.0 * softirq / total,
        "cpu_iowait": 100.0 * iowait / total,
    }
    if "tx" in before and "tx" in after and seconds > 0:
        out["nic_tx_mbit"] = (after["tx"] - before["tx"]) * 8 / seconds / 1e6
        out["nic_rx_mbit"] = (after["rx"] - before["rx"]) * 8 / seconds / 1e6
    if "tx_timeout" in before and "tx_timeout" in after:
        out["tx_timeouts"] = after["tx_timeout"] - before["tx_timeout"]
    return out


def containers(path):
    rows = []
    try:
        for l in open(path):
            l = l.strip()
            if not l:
                continue
            c = json.loads(l)
            rows.append({
                "name": c.get("Name", "?"),
                "cpu": _num(str(c.get("CPUPerc", "0")).rstrip("%")),
                "mem": c.get("MemUsage", "").split("/")[0].strip(),
                "pids": int(_num(c.get("PIDs"))),
            })
    except OSError:
        pass
    rows.sort(key=lambda r: -r["cpu"])
    return rows


def cmd_step(a):
    s = {"scenario": a.scenario, "rate": a.rate, "seconds": round(a.seconds, 1)}
    summ = os.path.join(a.dir, "summary.json")
    if not os.path.exists(summ):
        s.update(ok=False, why="k6 wrote no summary (see k6.log)")
        print(json.dumps(s))
        return
    k = k6_summary(summ)
    s.update(k)
    s.update(server_delta(read_probe(os.path.join(a.dir, "before.txt")),
                          read_probe(os.path.join(a.dir, "after.txt")), a.seconds))
    cs = containers(os.path.join(a.dir, "docker.jsonl"))
    s["containers"] = cs[:6]
    zipgo = [c for c in cs if "zipgo" in c["name"]]
    if zipgo:
        s["zipgo_pids"] = zipgo[0]["pids"]
    expected = a.rate * _num(a.step_seconds or 0) if a.step_seconds else None
    if expected is None:
        expected = k["iterations"] + k["dropped"]
    dropped = k["dropped"] / expected if expected else 0.0
    s["dropped_ratio"] = dropped
    p95 = k["iter_p95"] if a.scenario == "firstvisit" else k["req_p95"]
    s["p95"] = p95
    why = []
    if k["failed"] > 0.01:
        why.append(f"{k['failed'] * 100:.1f} % failed")
    if dropped > 0.02:
        why.append(f"{dropped * 100:.0f} % of arrivals dropped")
    if p95 > a.budget:
        why.append(f"p95 {p95:.0f} ms > {a.budget} ms")
    if s.get("tx_timeouts"):
        why.append(f"NIC TX timeout ×{s['tx_timeouts']}")
    s["ok"] = not why
    s["why"] = ", ".join(why)
    print(json.dumps(s))


def fmt_line(s):
    if "iterations" not in s:
        return f"  {s['scenario']:>10} @ {s['rate']:>5}/s  ✗ {s.get('why', '')}"
    top = s.get("containers") or []
    topc = f"{top[0]['name']} {top[0]['cpu']:.0f}%" if top else ""
    mark = "✓" if s["ok"] else "✗"
    return (f"  {s['scenario']:>10} @ {s['rate']:>5}/s  {mark} done {s['iter_rate']:7.1f}/s"
            f"  p50 {s['req_p50']:6.1f} p95 {s['p95']:7.1f} ms  fail {s['failed'] * 100:4.1f}%"
            f"  in {s['recv_rate'] * 8 / 1e6:6.1f} Mbit/s"
            + (f"  cpu {s['cpu_busy']:3.0f}% (sirq {s['cpu_softirq']:2.0f}%)" if "cpu_busy" in s else "")
            + (f"  [{topc}]" if topc else "")
            + (f"  — {s['why']}" if s["why"] else ""))


def cmd_line(a):
    print(fmt_line(json.loads(a.json)), flush=True)


UNIT = {"static": "req/s", "handshake": "new conns/s", "firstvisit": "visitors/s",
        "api-read": "req/s", "api-write": "writes/s", "mixed": "visitors/s"}
ORDER = ["static", "handshake", "firstvisit", "api-read", "api-write", "mixed"]


def load_run(d):
    meta = {}
    try:
        meta = json.load(open(os.path.join(d, "run.json")))
    except OSError:
        pass
    steps = []
    try:
        for l in open(os.path.join(d, "steps.jsonl")):
            if l.strip():
                steps.append(json.loads(l))
    except OSError:
        pass
    return meta, steps


def capacity(steps):
    """scenario → (best ok step, first failing step)."""
    cap = {}
    for s in steps:
        best, broke = cap.get(s["scenario"], (None, None))
        if s.get("ok") and (best is None or s["rate"] > best["rate"]):
            best = s
        if not s.get("ok") and broke is None:
            broke = s
        cap[s["scenario"]] = (best, broke)
    return cap


def cmd_report(a):
    runs = [(d,) + load_run(d) for d in a.dirs]
    out = []
    w = out.append
    w("# zipgo bench report\n")
    for d, meta, steps in runs:
        w(f"## {meta.get('label', os.path.basename(d))}\n")
        w(f"Site `{meta.get('site', '')}`, API `{meta.get('api', '') or '—'}`, "
          f"resolved to `{meta.get('resolve', '') or 'DNS'}`, {meta.get('step', '')} per step, "
          f"started {meta.get('started', '?')}.\n")
        cap = capacity(steps)
        w("| scenario | sustained | broke at | why |")
        w("|---|---|---|---|")
        for sc in ORDER:
            if sc not in cap:
                continue
            best, broke = cap[sc]
            b = f"**{best['rate']}** {UNIT[sc]}" if best else "—"
            k = f"{broke['rate']} {UNIT[sc]}" if broke else "not reached"
            w(f"| {sc} | {b} | {k} | {broke['why'] if broke else ''} |")
        mixed = cap.get("mixed", (None, None))[0]
        if mixed:
            people = mixed["rate"] * SESSION_MINUTES * 60
            w(f"\nMixed visitors sustained: {mixed['rate']}/s — with {SESSION_MINUTES}-minute "
              f"visits that is about **{people:,} people on the sites at once** "
              f"(arrival rate × visit length).\n")
        w("\n<details><summary>Every step</summary>\n")
        w("| scenario | rate | done/s | p50 ms | p95 ms | fail | dropped | in Mbit/s | CPU | softirq | NIC tx Mbit/s | top containers (CPU %) | zipgo pids |")
        w("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
        for s in steps:
            if "iterations" not in s:
                w(f"| {s['scenario']} | {s['rate']} | ✗ {s.get('why', '')} |||||||||||")
                continue
            top = ", ".join(f"{c['name']} {c['cpu']:.0f}" for c in (s.get("containers") or [])[:3])
            w(f"| {s['scenario']} | {s['rate']} | {s['iter_rate']:.1f} | {s['req_p50']:.1f} | {s['p95']:.0f} | "
              f"{s['failed'] * 100:.1f}% | {s.get('dropped_ratio', 0) * 100:.0f}% | {s['recv_rate'] * 8 / 1e6:.0f} | "
              f"{s.get('cpu_busy', 0):.0f}% | {s.get('cpu_softirq', 0):.0f}% | {s.get('nic_tx_mbit', 0):.0f} | "
              f"{top} | {s.get('zipgo_pids', '')} |")
        w("\n</details>\n")
    if len(runs) > 1:
        w("## Comparison\n")
        labels = [m.get("label", os.path.basename(d)) for d, m, _ in runs]
        caps = [capacity(s) for _, _, s in runs]
        w("| scenario | " + " | ".join(labels) + " |")
        w("|---|" + "---|" * len(labels))
        for sc in ORDER:
            if not any(sc in c for c in caps):
                continue
            cells = []
            ref = None
            for c in caps:
                best = c.get(sc, (None, None))[0]
                if best is None:
                    cells.append("—")
                    continue
                cell = f"{best['rate']} {UNIT[sc]}"
                if ref:
                    cell += f" ({best['rate'] / ref:.1f}×)"
                else:
                    ref = best["rate"]
                cells.append(cell)
            w(f"| {sc} | " + " | ".join(cells) + " |")
        # Bytes per visit, the compression win, from the first ok firstvisit step.
        w("\n| bytes per first visit | " + " | ".join(labels) + " |")
        w("|---|" + "---|" * len(labels))
        cells = []
        for _, _, steps in runs:
            fv = [s for s in steps if s["scenario"] == "firstvisit" and s.get("ok") and s.get("iter_rate")]
            cells.append(f"{fv[0]['recv_rate'] / fv[0]['iter_rate'] / 1024:.0f} KiB" if fv else "—")
        w("| received per visitor | " + " | ".join(cells) + " |")
    print("\n".join(out))


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("step")
    s.add_argument("--dir", required=True)
    s.add_argument("--scenario", required=True)
    s.add_argument("--rate", type=int, required=True)
    s.add_argument("--seconds", type=float, required=True)
    s.add_argument("--budget", type=float, required=True)
    s.add_argument("--step-seconds", type=float, default=None)
    l = sub.add_parser("line")
    l.add_argument("--json", required=True)
    r = sub.add_parser("report")
    r.add_argument("dirs", nargs="+")
    a = p.parse_args()
    {"step": cmd_step, "line": cmd_line, "report": cmd_report}[a.cmd](a)


if __name__ == "__main__":
    sys.exit(main())
