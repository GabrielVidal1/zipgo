package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A TLS test server that counts the connections it accepted.
func server(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var conns atomic.Int64
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Connection") != "close" && !r.Close {
			t.Errorf("request without Connection: close")
		}
		w.WriteHeader(status)
		io.WriteString(w, "<svg/>")
	}))
	ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts, &conns
}

func TestClosedLoopOpensOneConnectionPerRequest(t *testing.T) {
	ts, conns := server(t, 200)
	res, err := Run(context.Background(), Config{URL: ts.URL + "/vite.svg", Workers: 4, Duration: 400 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK == 0 || res.Fail != 0 {
		t.Fatalf("ok %d fail %d errors %v", res.OK, res.Fail, res.Errors)
	}
	if got := conns.Load(); got < res.OK {
		t.Errorf("%d requests answered over only %d connections: connections were reused", res.OK, got)
	}
	if res.Bytes == 0 || res.ConnsPerS <= 0 {
		t.Errorf("bytes %d rate %.1f", res.Bytes, res.ConnsPerS)
	}
	if !(res.Total.P50 <= res.Total.P95 && res.Total.P95 <= res.Total.P99) {
		t.Errorf("percentiles out of order: %+v", res.Total)
	}
}

func TestOpenLoopKeepsToTheRate(t *testing.T) {
	ts, _ := server(t, 200)
	res, err := Run(context.Background(), Config{URL: ts.URL + "/", Workers: 50, Rate: 100, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK < 60 || res.OK > 110 {
		t.Errorf("100 arrivals/s for 1 s answered %d", res.OK)
	}
}

func TestOpenLoopCountsArrivalsItCouldNotStart(t *testing.T) {
	slow := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	slow.StartTLS()
	defer slow.Close()
	res, err := Run(context.Background(), Config{URL: slow.URL + "/", Workers: 2, Rate: 200, Duration: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped == 0 {
		t.Errorf("2 workers on a 300 ms server at 200/s dropped nothing: %+v", res)
	}
}

func TestStatusAndVerification(t *testing.T) {
	ts, _ := server(t, 404)
	res, _ := Run(context.Background(), Config{URL: ts.URL + "/", Workers: 1, Duration: 200 * time.Millisecond})
	if res.OK != 0 || res.Fail == 0 || !strings.Contains(strings.Join(res.Errors, " "), "404") {
		t.Errorf("a 404 should fail: %+v", res)
	}
	ok, _ := server(t, 200)
	res, _ = Run(context.Background(), Config{URL: ok.URL + "/", Workers: 1, Duration: 200 * time.Millisecond, Verify: true})
	if res.OK != 0 || !strings.Contains(strings.Join(res.Errors, " "), "tls") {
		t.Errorf("-verify against a self-signed test certificate should fail the handshake: %+v", res)
	}
}

func TestRejectsNonHTTPS(t *testing.T) {
	if _, err := Run(context.Background(), Config{URL: "http://example.com/", Duration: time.Millisecond}); err == nil {
		t.Error("an http:// URL was accepted")
	}
}
