package tailscale

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

type fakeLocalAPIClient struct {
	status    *ipnstate.Status
	statusErr error
	serveCfg  *ipn.ServeConfig
	serveErr  error
}

func (f fakeLocalAPIClient) StatusWithoutPeers(context.Context) (*ipnstate.Status, error) {
	return f.status, f.statusErr
}

func (f fakeLocalAPIClient) GetServeConfig(context.Context) (*ipn.ServeConfig, error) {
	return f.serveCfg, f.serveErr
}

func TestResolveMissingLocalAPI(t *testing.T) {
	t.Parallel()

	inspector := &Inspector{
		localClient: fakeLocalAPIClient{
			statusErr: errors.New("localapi unavailable"),
		},
		now: func() time.Time {
			return time.Date(2026, 3, 30, 19, 0, 0, 0, time.UTC)
		},
	}

	status := inspector.Resolve(context.Background(), Options{
		LocalPort: intPtr(8888),
	})
	if status.Ready {
		t.Fatal("Resolve() reported ready without a reachable LocalAPI")
	}
	if status.SuggestedCommand != "tailscale funnel --bg --https=443 --set-path=/webhooks 8888" {
		t.Fatalf("SuggestedCommand = %q", status.SuggestedCommand)
	}
	if got := status.Checks[0].ID; got != "tailscale_local_api" {
		t.Fatalf("first check id = %q, want tailscale_local_api", got)
	}
}

func TestResolveDetectsMatchingFunnel(t *testing.T) {
	t.Parallel()

	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != readyzPath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer local.Close()

	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != readyzPath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer public.Close()

	localPort := mustURLPort(t, local.URL)
	inspector := &Inspector{
		localClient: fakeLocalAPIClient{
			status: &ipnstate.Status{
				BackendState: "Running",
				CurrentTailnet: &ipnstate.TailnetStatus{
					MagicDNSEnabled: true,
				},
			},
			serveCfg: &ipn.ServeConfig{
				Web: map[ipn.HostPort]*ipn.WebServerConfig{
					"colin.tail.example.ts.net:443": {
						Handlers: map[string]*ipn.HTTPHandler{
							"/webhooks": {Proxy: "http://127.0.0.1:" + itoa(localPort)},
						},
					},
				},
				AllowFunnel: map[ipn.HostPort]bool{
					"colin.tail.example.ts.net:443": true,
				},
			},
		},
		httpClient: public.Client(),
		now: func() time.Time {
			return time.Date(2026, 3, 30, 19, 0, 0, 0, time.UTC)
		},
	}

	status := inspector.Check(context.Background(), Options{
		LocalPort:                intPtr(localPort),
		LocalDashboardURL:        local.URL,
		ExplicitWebhookPublicURL: public.URL,
	})
	if !status.Ready {
		t.Fatalf("Check() Ready = false, checks = %#v", status.Checks)
	}
	if status.PublicBaseURL != public.URL {
		t.Fatalf("PublicBaseURL = %q, want %q", status.PublicBaseURL, public.URL)
	}
	if status.DetectedFunnelURL != "https://colin.tail.example.ts.net" {
		t.Fatalf("DetectedFunnelURL = %q", status.DetectedFunnelURL)
	}
	if status.GitHubWebhookURL != public.URL+githubWebhookPath {
		t.Fatalf("GitHubWebhookURL = %q", status.GitHubWebhookURL)
	}
}

func TestResolveRejectsNonWebhookMount(t *testing.T) {
	t.Parallel()

	inspector := &Inspector{
		localClient: fakeLocalAPIClient{
			status: &ipnstate.Status{
				BackendState: "Running",
				CurrentTailnet: &ipnstate.TailnetStatus{
					MagicDNSEnabled: true,
				},
			},
			serveCfg: &ipn.ServeConfig{
				Web: map[ipn.HostPort]*ipn.WebServerConfig{
					"colin.tail.example.ts.net:443": {
						Handlers: map[string]*ipn.HTTPHandler{
							"/": {Proxy: "http://127.0.0.1:8888"},
						},
					},
				},
				AllowFunnel: map[ipn.HostPort]bool{
					"colin.tail.example.ts.net:443": true,
				},
			},
		},
		now: func() time.Time {
			return time.Date(2026, 3, 30, 19, 0, 0, 0, time.UTC)
		},
	}

	status := inspector.Resolve(context.Background(), Options{
		LocalPort: intPtr(8888),
	})
	if status.Ready {
		t.Fatal("Resolve() reported ready for non-webhook funnel mount")
	}
	last := status.Checks[len(status.Checks)-1]
	if last.ID != "funnel_route" {
		t.Fatalf("last check id = %q, want funnel_route", last.ID)
	}
	if !strings.Contains(last.Detail, "/webhooks") {
		t.Fatalf("funnel detail = %q", last.Detail)
	}
}

func TestResolveDerivesPublicURLFromFunnelWhenUnset(t *testing.T) {
	t.Parallel()

	inspector := &Inspector{
		localClient: fakeLocalAPIClient{
			status: &ipnstate.Status{
				BackendState: "Running",
				CurrentTailnet: &ipnstate.TailnetStatus{
					MagicDNSEnabled: true,
				},
			},
			serveCfg: &ipn.ServeConfig{
				Web: map[ipn.HostPort]*ipn.WebServerConfig{
					"colin.tail.example.ts.net:8443": {
						Handlers: map[string]*ipn.HTTPHandler{
							"/webhooks": {Proxy: "http://127.0.0.1:8888"},
						},
					},
				},
				AllowFunnel: map[ipn.HostPort]bool{
					"colin.tail.example.ts.net:8443": true,
				},
			},
		},
		now: func() time.Time {
			return time.Date(2026, 3, 30, 19, 0, 0, 0, time.UTC)
		},
	}

	status := inspector.Resolve(context.Background(), Options{
		LocalPort: intPtr(8888),
	})
	if status.PublicBaseURL != "https://colin.tail.example.ts.net:8443" {
		t.Fatalf("PublicBaseURL = %q", status.PublicBaseURL)
	}
	if status.PublicURLSource != "funnel" {
		t.Fatalf("PublicURLSource = %q", status.PublicURLSource)
	}
}

func TestResolveHandlesMissingCurrentTailnet(t *testing.T) {
	t.Parallel()

	inspector := &Inspector{
		localClient: fakeLocalAPIClient{
			status: &ipnstate.Status{
				BackendState: "Running",
			},
			serveCfg: &ipn.ServeConfig{},
		},
		now: func() time.Time {
			return time.Date(2026, 3, 30, 19, 0, 0, 0, time.UTC)
		},
	}

	status := inspector.Resolve(context.Background(), Options{
		LocalPort: intPtr(8888),
	})
	if status.Ready {
		t.Fatal("Resolve() reported ready without a current tailnet")
	}
	if got := status.Checks[2].ID; got != "magic_dns" {
		t.Fatalf("check id = %q, want magic_dns", got)
	}
	if got := status.Checks[2].Detail; got != "The local Tailscale daemon is not connected to a tailnet yet." {
		t.Fatalf("magic dns detail = %q", got)
	}
}

func mustURLPort(t *testing.T, raw string) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(raw, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", raw, err)
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		t.Fatalf("LookupPort(%q) error = %v", portText, err)
	}
	return port
}

func intPtr(value int) *int {
	return &value
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
