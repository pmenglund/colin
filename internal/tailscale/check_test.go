package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResolveMissingBinary(t *testing.T) {
	t.Parallel()

	inspector := &Inspector{
		lookPath: func(string) (string, error) { return "", errors.New("missing") },
		now: func() time.Time {
			return time.Date(2026, 3, 30, 19, 0, 0, 0, time.UTC)
		},
	}

	status := inspector.Resolve(context.Background(), Options{
		LocalPort: intPtr(8888),
	})
	if status.Ready {
		t.Fatal("Resolve() reported ready without tailscale")
	}
	if status.SuggestedCommand != "tailscale funnel --bg --https=443 --set-path=/webhooks 8888" {
		t.Fatalf("SuggestedCommand = %q", status.SuggestedCommand)
	}
	if got := status.Checks[0].ID; got != "tailscale_cli" {
		t.Fatalf("first check id = %q, want tailscale_cli", got)
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
		lookPath: func(string) (string, error) { return "/usr/local/bin/tailscale", nil },
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "status --json":
				return marshalJSON(t, map[string]any{
					"BackendState": "Running",
					"Self": map[string]any{
						"DNSName": "colin.tail.example.ts.net.",
					},
					"CurrentTailnet": map[string]any{
						"MagicDNSEnabled": true,
					},
				}), nil
			case "funnel status --json":
				return marshalJSON(t, map[string]any{
					"Web": map[string]any{
						"colin.tail.example.ts.net:443": map[string]any{
							"Handlers": map[string]any{
								"/webhooks": map[string]any{
									"Proxy": "http://127.0.0.1:" + itoa(localPort),
								},
							},
						},
					},
					"AllowFunnel": map[string]bool{
						"colin.tail.example.ts.net:443": true,
					},
				}), nil
			default:
				t.Fatalf("unexpected args: %v", args)
				return nil, nil
			}
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
		lookPath: func(string) (string, error) { return "/usr/local/bin/tailscale", nil },
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "status --json":
				return marshalJSON(t, map[string]any{
					"BackendState": "Running",
					"CurrentTailnet": map[string]any{
						"MagicDNSEnabled": true,
					},
				}), nil
			case "funnel status --json":
				return marshalJSON(t, map[string]any{
					"Web": map[string]any{
						"colin.tail.example.ts.net:443": map[string]any{
							"Handlers": map[string]any{
								"/": map[string]any{
									"Proxy": "http://127.0.0.1:8888",
								},
							},
						},
					},
					"AllowFunnel": map[string]bool{
						"colin.tail.example.ts.net:443": true,
					},
				}), nil
			default:
				t.Fatalf("unexpected args: %v", args)
				return nil, nil
			}
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
		lookPath: func(string) (string, error) { return "/usr/local/bin/tailscale", nil },
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "status --json":
				return marshalJSON(t, map[string]any{
					"BackendState": "Running",
					"CurrentTailnet": map[string]any{
						"MagicDNSEnabled": true,
					},
				}), nil
			case "funnel status --json":
				return marshalJSON(t, map[string]any{
					"Web": map[string]any{
						"colin.tail.example.ts.net:8443": map[string]any{
							"Handlers": map[string]any{
								"/webhooks": map[string]any{
									"Proxy": "http://127.0.0.1:8888",
								},
							},
						},
					},
					"AllowFunnel": map[string]bool{
						"colin.tail.example.ts.net:8443": true,
					},
				}), nil
			default:
				t.Fatalf("unexpected args: %v", args)
				return nil, nil
			}
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

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
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
