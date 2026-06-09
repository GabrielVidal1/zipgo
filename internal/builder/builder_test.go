package builder

import (
	"testing"
)

func TestMetricsAddr(t *testing.T) {
	t.Setenv("ZIPGO_METRICS", "")
	t.Setenv("ZIPGO_METRICS_ADDR", "")
	if got := MetricsAddr(); got != "" {
		t.Fatalf("metrics off: want \"\", got %q", got)
	}

	t.Setenv("ZIPGO_METRICS", "1")
	if got := MetricsAddr(); got != DefaultMetricsAddr {
		t.Fatalf("metrics on: want %q, got %q", DefaultMetricsAddr, got)
	}

	t.Setenv("ZIPGO_METRICS_ADDR", "0.0.0.0:9100")
	if got := MetricsAddr(); got != "0.0.0.0:9100" {
		t.Fatalf("custom addr: want 0.0.0.0:9100, got %q", got)
	}
}

func TestWithMetrics(t *testing.T) {
	// Disabled: no-op, no metrics server, no per-server flag.
	servers := obj{"sites": obj{"listen": arr{":9000"}}}
	withMetrics(servers, "")
	if _, ok := servers["metrics"]; ok {
		t.Fatal("disabled: should not add a metrics server")
	}
	if _, ok := servers["sites"].(obj)["metrics"]; ok {
		t.Fatal("disabled: should not enable per-server metrics")
	}

	// Enabled: adds dedicated server + per-server metrics flag.
	servers = obj{"sites": obj{"listen": arr{":9000"}}}
	withMetrics(servers, "127.0.0.1:2019")
	ms, ok := servers["metrics"].(obj)
	if !ok {
		t.Fatal("enabled: missing metrics server")
	}
	if got := ms["listen"].(arr)[0]; got != "127.0.0.1:2019" {
		t.Fatalf("metrics server listen: got %v", got)
	}
	h := ms["routes"].(arr)[0].(obj)["handle"].(arr)[0].(obj)["handler"]
	if h != "metrics" {
		t.Fatalf("metrics route handler: want metrics, got %v", h)
	}
	if _, ok := servers["sites"].(obj)["metrics"]; !ok {
		t.Fatal("enabled: per-server metrics flag not set")
	}
}

func TestBuildConfigWithMetrics(t *testing.T) {
	cfg, err := BuildConfig(nil, "127.0.0.1:2019")
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if cfg.AppsRaw["http"] == nil {
		t.Fatal("missing http app")
	}
}
