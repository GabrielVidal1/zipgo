// connload — the bench's new-connection load generator (see connload.go).
//
//	go run ./bench/connload -url https://bench.example.com/vite.svg -resolve 192.168.1.131 \
//	    [-c 64] [-d 20s] [-rate 400] [-verify] [-resume] [-x25519] [-json out.json]
//
// Without -rate: a closed loop of -c connections (the ceiling). With -rate:
// that many arrivals per second, at most -c at once (the arrivals it could
// not start are counted as dropped, like k6's dropped_iterations).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var c Config
	var out string
	flag.StringVar(&c.URL, "url", "", "https URL to fetch (one small file)")
	flag.StringVar(&c.Resolve, "resolve", "", "IP to connect to instead of resolving the host")
	flag.IntVar(&c.Workers, "c", 64, "connections in flight (closed loop), or the most at once (with -rate)")
	flag.DurationVar(&c.Duration, "d", 20*time.Second, "how long to run")
	flag.Float64Var(&c.Rate, "rate", 0, "arrivals per second (open loop); 0 = closed loop")
	flag.BoolVar(&c.Verify, "verify", false, "verify the server's certificate chain (client CPU, not server work)")
	flag.BoolVar(&c.Resume, "resume", false, "resume TLS sessions, like a returning visitor")
	flag.BoolVar(&c.X25519, "x25519", false, "offer X25519 only, not the post-quantum hybrid browsers send")
	flag.StringVar(&out, "json", "", "also write the result as JSON to this file")
	flag.Parse()

	res, err := Run(context.Background(), c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connload:", err)
		os.Exit(2)
	}
	if out != "" {
		b, _ := json.MarshalIndent(res, "", " ")
		if err := os.WriteFile(out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "connload:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("%.0f new conns/s  ok %d  fail %d  dropped %d  connect p50/p95 %.1f/%.1f ms  tls p50/p95 %.1f/%.1f ms  total p50/p95/p99 %.1f/%.1f/%.1f ms\n",
		res.ConnsPerS, res.OK, res.Fail, res.Dropped, res.Connect.P50, res.Connect.P95,
		res.TLS.P50, res.TLS.P95, res.Total.P50, res.Total.P95, res.Total.P99)
	for _, e := range res.Errors {
		fmt.Println("  error:", e)
	}
}
