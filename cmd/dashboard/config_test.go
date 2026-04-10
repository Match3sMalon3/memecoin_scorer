package main

import (
	"os"
	"testing"
)

func setenv(t *testing.T, key, val string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("Setenv(%q): %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func unsetenv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		}
	})
}

func TestResolveConfig_Defaults(t *testing.T) {
	unsetenv(t, "LIVE_MODE")
	unsetenv(t, "INGESTOR_URL")
	unsetenv(t, "REFRESH_INTERVAL_SEC")
	unsetenv(t, "DASHBOARD_PORT")
	unsetenv(t, "PORT")

	cfg := resolveConfig()

	if cfg.liveMode {
		t.Error("liveMode should be false by default")
	}
	if cfg.ingestorURL != "http://localhost:8080" {
		t.Errorf("ingestorURL = %q, want http://localhost:8080", cfg.ingestorURL)
	}
	if cfg.refreshInterval != 10 {
		t.Errorf("refreshInterval = %d, want 10", cfg.refreshInterval)
	}
	if cfg.listenPort != "8090" {
		t.Errorf("listenPort = %q, want 8090", cfg.listenPort)
	}
}

func TestResolveConfig_LiveMode(t *testing.T) {
	setenv(t, "LIVE_MODE", "1")
	unsetenv(t, "INGESTOR_URL")
	unsetenv(t, "REFRESH_INTERVAL_SEC")

	cfg := resolveConfig()

	if !cfg.liveMode {
		t.Error("liveMode should be true when LIVE_MODE=1")
	}
}

func TestResolveConfig_LiveMode_ZeroNotEnabled(t *testing.T) {
	setenv(t, "LIVE_MODE", "0")

	cfg := resolveConfig()

	if cfg.liveMode {
		t.Error("liveMode should be false when LIVE_MODE=0")
	}
}

func TestResolveConfig_LiveMode_EmptyNotEnabled(t *testing.T) {
	unsetenv(t, "LIVE_MODE")

	cfg := resolveConfig()

	if cfg.liveMode {
		t.Error("liveMode should be false when LIVE_MODE is unset")
	}
}

func TestResolveConfig_CustomIngestorURL(t *testing.T) {
	setenv(t, "INGESTOR_URL", "http://10.0.0.5:9090")

	cfg := resolveConfig()

	if cfg.ingestorURL != "http://10.0.0.5:9090" {
		t.Errorf("ingestorURL = %q, want http://10.0.0.5:9090", cfg.ingestorURL)
	}
}

func TestResolveConfig_CustomRefreshInterval(t *testing.T) {
	setenv(t, "REFRESH_INTERVAL_SEC", "30")

	cfg := resolveConfig()

	if cfg.refreshInterval != 30 {
		t.Errorf("refreshInterval = %d, want 30", cfg.refreshInterval)
	}
}

func TestResolveConfig_InvalidRefreshInterval_UsesDefault(t *testing.T) {
	setenv(t, "REFRESH_INTERVAL_SEC", "notanumber")

	cfg := resolveConfig()

	if cfg.refreshInterval != 10 {
		t.Errorf("refreshInterval = %d, want 10 (default) for invalid value", cfg.refreshInterval)
	}
}

func TestResolveConfig_ZeroRefreshInterval_UsesDefault(t *testing.T) {
	setenv(t, "REFRESH_INTERVAL_SEC", "0")

	cfg := resolveConfig()

	if cfg.refreshInterval != 10 {
		t.Errorf("refreshInterval = %d, want 10 (default) for zero value", cfg.refreshInterval)
	}
}

// ---- port resolution tests ----

func TestResolveDashboardPort_Default(t *testing.T) {
	unsetenv(t, "DASHBOARD_PORT")
	unsetenv(t, "PORT")
	if got := resolveDashboardPort(); got != "8090" {
		t.Errorf("resolveDashboardPort() = %q, want 8090", got)
	}
}

func TestResolveDashboardPort_DashboardPortWins(t *testing.T) {
	setenv(t, "DASHBOARD_PORT", "9090")
	setenv(t, "PORT", "8080") // PORT must not override DASHBOARD_PORT
	if got := resolveDashboardPort(); got != "9090" {
		t.Errorf("resolveDashboardPort() = %q, want 9090 (DASHBOARD_PORT wins over PORT)", got)
	}
}

func TestResolveDashboardPort_LegacyPort(t *testing.T) {
	unsetenv(t, "DASHBOARD_PORT")
	setenv(t, "PORT", "7070")
	if got := resolveDashboardPort(); got != "7070" {
		t.Errorf("resolveDashboardPort() = %q, want 7070 (PORT fallback)", got)
	}
}

// Critical regression guard: when the ingestor sets PORT=8080 and the dashboard
// is started with DASHBOARD_PORT=8090, the dashboard must bind to 8090, not 8080.
func TestResolveDashboardPort_IngestorPortEnvDoesNotLeak(t *testing.T) {
	setenv(t, "DASHBOARD_PORT", "8090")
	setenv(t, "PORT", "8080") // simulates ingestor env leaking into dashboard
	if got := resolveDashboardPort(); got != "8090" {
		t.Errorf("resolveDashboardPort() = %q, want 8090 — PORT=8080 must not leak when DASHBOARD_PORT is set", got)
	}
}

func TestResolveConfig_ListenPortFromDashboardPort(t *testing.T) {
	setenv(t, "DASHBOARD_PORT", "8091")
	unsetenv(t, "PORT")
	cfg := resolveConfig()
	if cfg.listenPort != "8091" {
		t.Errorf("listenPort = %q, want 8091", cfg.listenPort)
	}
}
