// Package main is connload, the bench's new-connection load generator: each
// arrival opens a fresh TCP + TLS connection, sends one GET with
// "Connection: close", reads the answer and closes. It exists because k6 is
// too heavy a client for this one job — on a 4-core laptop CPU k6 tops out
// around 770 new connections/s even without certificate checks, and around
// 350/s with them, so a server that does 1,400/s looks like it does 350/s.
//
// Certificate verification is off by default. It is pure client work (the
// server does the same handshake either way) and expensive: Let's Encrypt's
// intermediates carry P-384 keys, which Go verifies in generic code, ~14 ms
// of CPU per connection on that laptop. Real visitors verify on their own
// devices; a load generator verifying for thousands of them measures itself.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config is one run.
type Config struct {
	URL      string        // https://host[:port]/path
	Resolve  string        // connect to this IP instead of resolving the host
	Workers  int           // closed loop: connections in flight; open loop: the most at once
	Duration time.Duration //
	Rate     float64       // > 0: open loop, this many arrivals per second
	Verify   bool          // verify the certificate chain (costly on the client)
	Resume   bool          // resume TLS sessions, like a returning visitor
	X25519   bool          // offer X25519 only (no post-quantum hybrid)
}

// Percentiles in milliseconds.
type Percentiles struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

// Result is what report.py reads (connload.json).
type Result struct {
	ConnsPerS float64     `json:"conns_per_s"`
	OK        int64       `json:"ok"`
	Fail      int64       `json:"fail"`
	Dropped   int64       `json:"dropped"` // open loop: arrivals not started, every worker busy
	Seconds   float64     `json:"seconds"`
	Bytes     int64       `json:"bytes"` // received, TLS payload
	Connect   Percentiles `json:"connect_ms"`
	TLS       Percentiles `json:"tls_ms"`
	Total     Percentiles `json:"total_ms"`
	Errors    []string    `json:"errors"` // distinct, at most 10
}

type sample struct{ connect, handshake, total time.Duration }

type runner struct {
	addr, host string
	request    []byte
	tls        *tls.Config

	ok, fail, bytes atomic.Int64
	mu              sync.Mutex
	samples         []sample
	errs            map[string]bool
}

func newRunner(c Config) (*runner, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("need an https URL, got %q", c.URL)
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	dial := host
	if c.Resolve != "" {
		dial = c.Resolve
	}
	hostHeader := host
	if u.Port() != "" {
		hostHeader = u.Host
	}
	cfg := &tls.Config{ServerName: host, InsecureSkipVerify: !c.Verify, NextProtos: []string{"http/1.1"}}
	if c.Resume {
		cfg.ClientSessionCache = tls.NewLRUClientSessionCache(4096)
	}
	if c.X25519 {
		cfg.CurvePreferences = []tls.CurveID{tls.X25519}
	}
	return &runner{
		addr: net.JoinHostPort(dial, port),
		host: host,
		request: []byte("GET " + u.RequestURI() + " HTTP/1.1\r\nHost: " + hostHeader +
			"\r\nUser-Agent: zipgo-bench connload\r\nAccept-Encoding: gzip\r\nConnection: close\r\n\r\n"),
		tls:  cfg,
		errs: map[string]bool{},
	}, nil
}

func (r *runner) failed(kind string, err error) {
	r.fail.Add(1)
	msg := err.Error()
	// "read tcp 10.0.0.1:51234->10.0.0.2:443: connection reset by peer" names
	// the local port: keep the reason only, one line per kind of failure.
	if strings.Contains(msg, "->") {
		msg = strings.TrimSpace(msg[strings.LastIndex(msg, ":")+1:])
	}
	r.mu.Lock()
	if len(r.errs) < 10 {
		r.errs[kind+": "+msg] = true
	}
	r.mu.Unlock()
}

// one connection: dial, handshake, GET, read to EOF, close.
func (r *runner) one() {
	t0 := time.Now()
	c, err := net.DialTimeout("tcp", r.addr, 10*time.Second)
	if err != nil {
		r.failed("dial", err)
		return
	}
	defer c.Close()
	t1 := time.Now()
	_ = c.SetDeadline(t1.Add(15 * time.Second))
	tc := tls.Client(c, r.tls)
	if err := tc.Handshake(); err != nil {
		r.failed("tls", err)
		return
	}
	t2 := time.Now()
	if _, err := tc.Write(r.request); err != nil {
		r.failed("write", err)
		return
	}
	cr := &countReader{r: tc}
	br := bufio.NewReader(cr)
	line, err := br.ReadString('\n')
	if err == nil && (len(line) < 12 || line[9:12] != "200") {
		err = fmt.Errorf("status %q", strings.TrimSpace(line))
	}
	if err == nil {
		_, err = io.Copy(io.Discard, br)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		r.failed("http", err)
		return
	}
	t3 := time.Now()
	r.ok.Add(1)
	r.bytes.Add(cr.n)
	r.mu.Lock()
	r.samples = append(r.samples, sample{t1.Sub(t0), t2.Sub(t1), t3.Sub(t0)})
	r.mu.Unlock()
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Run drives the load and returns the result.
func Run(ctx context.Context, c Config) (Result, error) {
	r, err := newRunner(c)
	if err != nil {
		return Result{}, err
	}
	if c.Workers < 1 {
		c.Workers = 1
	}
	ctx, cancel := context.WithTimeout(ctx, c.Duration)
	defer cancel()
	start := time.Now()
	var wg sync.WaitGroup
	var dropped int64
	if c.Rate > 0 {
		slots := make(chan struct{}, c.Workers)
		tick := time.Duration(float64(time.Second) / c.Rate)
		next := time.Now()
	arrivals:
		for {
			select {
			case <-ctx.Done():
				break arrivals
			default:
			}
			select {
			case slots <- struct{}{}:
				wg.Add(1)
				go func() { defer wg.Done(); r.one(); <-slots }()
			default:
				dropped++
			}
			next = next.Add(tick)
			if d := time.Until(next); d > 0 {
				select {
				case <-ctx.Done():
					break arrivals
				case <-time.After(d):
				}
			}
		}
	} else {
		for i := 0; i < c.Workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ctx.Err() == nil {
					r.one()
				}
			}()
		}
	}
	wg.Wait()
	secs := time.Since(start).Seconds()

	res := Result{OK: r.ok.Load(), Fail: r.fail.Load(), Dropped: dropped, Seconds: secs, Bytes: r.bytes.Load()}
	if secs > 0 {
		res.ConnsPerS = float64(res.OK) / secs
	}
	res.Connect = percentiles(r.samples, func(s sample) time.Duration { return s.connect })
	res.TLS = percentiles(r.samples, func(s sample) time.Duration { return s.handshake })
	res.Total = percentiles(r.samples, func(s sample) time.Duration { return s.total })
	res.Errors = []string{}
	for e := range r.errs {
		res.Errors = append(res.Errors, e)
	}
	sort.Strings(res.Errors)
	return res, nil
}

func percentiles(s []sample, f func(sample) time.Duration) Percentiles {
	if len(s) == 0 {
		return Percentiles{}
	}
	v := make([]time.Duration, len(s))
	for i, x := range s {
		v[i] = f(x)
	}
	sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
	at := func(p float64) float64 { return float64(v[int(p*float64(len(v)-1))].Microseconds()) / 1000 }
	return Percentiles{P50: at(.50), P95: at(.95), P99: at(.99)}
}
