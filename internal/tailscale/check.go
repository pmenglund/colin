package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

const (
	setupPath         = "/setup/funnel"
	webhookMountPath  = "/webhooks"
	readyzPath        = "/webhooks/readyz"
	linearWebhookPath = "/webhooks/linear"
	githubWebhookPath = "/webhooks/github"
)

var allowedFunnelPorts = []int{443, 8443, 10000}

// Options configures one Tailscale Funnel readiness inspection.
type Options struct {
	LocalPort                *int
	LocalDashboardURL        string
	ExplicitWebhookPublicURL string
}

// Inspector inspects local Tailscale state and Colin reachability.
type Inspector struct {
	lookPath   func(string) (string, error)
	run        func(context.Context, string, ...string) ([]byte, error)
	httpClient *http.Client
	now        func() time.Time
}

// NewInspector returns the default Tailscale inspector.
func NewInspector() *Inspector {
	return &Inspector{
		lookPath: exec.LookPath,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		httpClient: &http.Client{Timeout: 3 * time.Second},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Resolve returns local Tailscale-derived readiness details without network probes.
func (i *Inspector) Resolve(ctx context.Context, opts Options) domain.FunnelSetupStatus {
	if i == nil {
		i = NewInspector()
	}
	now := i.now()
	status := domain.FunnelSetupStatus{
		GeneratedAt:  now,
		LocalBaseURL: localBaseURL(opts),
	}
	status.LocalSetupURL = joinURL(status.LocalBaseURL, setupPath)
	status.LocalReadyURL = joinURL(status.LocalBaseURL, readyzPath)

	checks := make([]domain.SetupCheck, 0, 4)
	addCheck := func(id, label, checkStatus, detail, remediation string) {
		checks = append(checks, domain.SetupCheck{
			ID:          id,
			Label:       label,
			Status:      checkStatus,
			Detail:      strings.TrimSpace(detail),
			Remediation: strings.TrimSpace(remediation),
			CheckedAt:   now,
		})
	}

	binaryPath, err := i.lookPath("tailscale")
	if err != nil {
		addCheck(
			"tailscale_cli",
			"Tailscale CLI is installed",
			"error",
			"`tailscale` is not available on PATH.",
			"Install Tailscale and ensure the `tailscale` CLI is available before configuring Funnel.",
		)
		status.SuggestedCommand = suggestedCommand(nil, opts.LocalPort)
		status.Checks = checks
		return finalizeStatus(status)
	}
	addCheck("tailscale_cli", "Tailscale CLI is installed", "ok", fmt.Sprintf("Using `%s`.", binaryPath), "")

	tsStatus, err := i.readStatus(ctx)
	if err != nil {
		addCheck(
			"tailscale_backend",
			"Tailscale is running",
			"error",
			err.Error(),
			"Start Tailscale and confirm `tailscale status --json` succeeds.",
		)
		status.SuggestedCommand = suggestedCommand(nil, opts.LocalPort)
		status.Checks = checks
		return finalizeStatus(status)
	}
	if strings.EqualFold(strings.TrimSpace(tsStatus.BackendState), "Running") {
		addCheck("tailscale_backend", "Tailscale is running", "ok", "Backend state is `Running`.", "")
	} else {
		addCheck(
			"tailscale_backend",
			"Tailscale is running",
			"error",
			fmt.Sprintf("Backend state is `%s`.", strings.TrimSpace(tsStatus.BackendState)),
			"Connect this machine to Tailscale before enabling Funnel.",
		)
	}
	if tsStatus.CurrentTailnet.MagicDNSEnabled {
		addCheck("magic_dns", "MagicDNS is enabled", "ok", "MagicDNS is enabled for the current tailnet.", "")
	} else {
		addCheck(
			"magic_dns",
			"MagicDNS is enabled",
			"error",
			"MagicDNS is disabled for the current tailnet.",
			"Enable MagicDNS in the Tailscale admin console before enabling Funnel.",
		)
	}

	serveCfg, err := i.readFunnelStatus(ctx)
	if err != nil {
		addCheck(
			"funnel_route",
			"Funnel proxies Colin at `/webhooks`",
			"error",
			err.Error(),
			"Run the suggested `tailscale funnel` command after Tailscale is healthy.",
		)
		status.SuggestedCommand = suggestedCommand(nil, opts.LocalPort)
		status.Checks = checks
		return finalizeStatus(status)
	}

	match, usedPorts, matchErr := matchFunnel(serveCfg, opts.LocalPort)
	status.SuggestedCommand = suggestedCommand(usedPorts, opts.LocalPort)
	if matchErr != nil {
		addCheck(
			"funnel_route",
			"Funnel proxies Colin at `/webhooks`",
			"error",
			matchErr.Error(),
			funnelRemediation(status.SuggestedCommand),
		)
	} else {
		status.DetectedFunnelURL = match.URL
		addCheck(
			"funnel_route",
			"Funnel proxies Colin at `/webhooks`",
			"ok",
			fmt.Sprintf("Detected `%s` proxying Colin from `/webhooks`.", match.URL),
			"",
		)
	}

	if value := strings.TrimSpace(opts.ExplicitWebhookPublicURL); value != "" {
		status.PublicBaseURL = strings.TrimRight(value, "/")
		status.PublicURLSource = "config"
	} else if match != nil {
		status.PublicBaseURL = strings.TrimRight(match.URL, "/")
		status.PublicURLSource = "funnel"
	}
	status.PublicReadyURL = joinURL(status.PublicBaseURL, readyzPath)
	status.LinearWebhookURL = joinURL(status.PublicBaseURL, linearWebhookPath)
	status.GitHubWebhookURL = joinURL(status.PublicBaseURL, githubWebhookPath)
	status.Checks = checks
	return finalizeStatus(status)
}

// Check returns full readiness details, including local and public HTTP probes.
func (i *Inspector) Check(ctx context.Context, opts Options) domain.FunnelSetupStatus {
	status := i.Resolve(ctx, opts)
	now := status.GeneratedAt

	status.Checks = append(status.Checks, probeCheck(i.httpClient, now, "local_readyz", "Colin responds locally at `/webhooks/readyz`", status.LocalReadyURL, "Start Colin so it serves the local readiness endpoint before enabling Funnel."))
	status.Checks = append(status.Checks, probeCheck(i.httpClient, now, "public_readyz", "Colin responds publicly at `/webhooks/readyz`", status.PublicReadyURL, publicRemediation(status.SuggestedCommand)))
	return finalizeStatus(status)
}

type statusResponse struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		DNSName string `json:"DNSName"`
	} `json:"Self"`
	CurrentTailnet struct {
		MagicDNSEnabled bool `json:"MagicDNSEnabled"`
	} `json:"CurrentTailnet"`
}

type serveConfig struct {
	Web         map[string]webServerConfig `json:"Web"`
	AllowFunnel map[string]bool            `json:"AllowFunnel"`
}

type webServerConfig struct {
	Handlers map[string]httpHandler `json:"Handlers"`
}

type httpHandler struct {
	Path     string `json:"Path"`
	Proxy    string `json:"Proxy"`
	Text     string `json:"Text"`
	Redirect string `json:"Redirect"`
}

type funnelMatchInfo struct {
	URL      string
	HostPort string
}

func (i *Inspector) readStatus(ctx context.Context) (statusResponse, error) {
	output, err := i.run(ctx, "tailscale", "status", "--json")
	if err != nil {
		return statusResponse{}, cliError("tailscale status --json", output, err)
	}
	var out statusResponse
	if err := json.Unmarshal(output, &out); err != nil {
		return statusResponse{}, fmt.Errorf("parse tailscale status JSON: %w", err)
	}
	return out, nil
}

func (i *Inspector) readFunnelStatus(ctx context.Context) (serveConfig, error) {
	output, err := i.run(ctx, "tailscale", "funnel", "status", "--json")
	if err != nil {
		return serveConfig{}, cliError("tailscale funnel status --json", output, err)
	}
	var out serveConfig
	if err := json.Unmarshal(output, &out); err != nil {
		return serveConfig{}, fmt.Errorf("parse tailscale funnel JSON: %w", err)
	}
	return out, nil
}

func matchFunnel(cfg serveConfig, localPort *int) (*funnelMatchInfo, map[int]struct{}, error) {
	if localPort == nil || *localPort <= 0 {
		return nil, usedPorts(cfg), errors.New("Colin needs a fixed local `server.port` before Funnel can be configured.")
	}

	ports := usedPorts(cfg)
	var sawProxyForPort bool
	for hostPort, server := range cfg.Web {
		handler, ok := server.Handlers[webhookMountPath]
		if !ok {
			for _, other := range server.Handlers {
				if proxyTargetsPort(other.Proxy, *localPort) {
					sawProxyForPort = true
					break
				}
			}
			continue
		}
		if !proxyTargetsPort(handler.Proxy, *localPort) {
			continue
		}
		sawProxyForPort = true
		if !cfg.AllowFunnel[hostPort] {
			continue
		}
		return &funnelMatchInfo{
			URL:      hostPortToURL(hostPort),
			HostPort: hostPort,
		}, ports, nil
	}
	if sawProxyForPort {
		return nil, ports, errors.New("Funnel points at Colin, but not from `/webhooks`.")
	}
	return nil, ports, fmt.Errorf("No Funnel proxies `127.0.0.1:%d` from `/webhooks` yet.", *localPort)
}

func usedPorts(cfg serveConfig) map[int]struct{} {
	out := map[int]struct{}{}
	for hostPort := range cfg.Web {
		if port, ok := parseHostPortPort(hostPort); ok {
			out[port] = struct{}{}
		}
	}
	for hostPort := range cfg.AllowFunnel {
		if port, ok := parseHostPortPort(hostPort); ok {
			out[port] = struct{}{}
		}
	}
	return out
}

func probeCheck(client *http.Client, checkedAt time.Time, id, label, rawURL, remediation string) domain.SetupCheck {
	if strings.TrimSpace(rawURL) == "" {
		return domain.SetupCheck{
			ID:          id,
			Label:       label,
			Status:      "error",
			Detail:      "No URL is available yet for this check.",
			Remediation: remediation,
			CheckedAt:   checkedAt,
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return domain.SetupCheck{
			ID:          id,
			Label:       label,
			Status:      "error",
			Detail:      err.Error(),
			Remediation: remediation,
			CheckedAt:   checkedAt,
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return domain.SetupCheck{
			ID:        id,
			Label:     label,
			Status:    "ok",
			Detail:    fmt.Sprintf("`%s` returned `%d`.", rawURL, resp.StatusCode),
			CheckedAt: checkedAt,
		}
	}
	return domain.SetupCheck{
		ID:          id,
		Label:       label,
		Status:      "error",
		Detail:      fmt.Sprintf("`%s` returned `%d`.", rawURL, resp.StatusCode),
		Remediation: remediation,
		CheckedAt:   checkedAt,
	}
}

func finalizeStatus(status domain.FunnelSetupStatus) domain.FunnelSetupStatus {
	ready := len(status.Checks) > 0
	for _, check := range status.Checks {
		if check.Status != "ok" {
			ready = false
			break
		}
	}
	status.Ready = ready
	return status
}

func localBaseURL(opts Options) string {
	if value := strings.TrimSpace(opts.LocalDashboardURL); value != "" {
		return strings.TrimRight(value, "/")
	}
	if opts.LocalPort == nil || *opts.LocalPort <= 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", *opts.LocalPort)
}

func joinURL(base, suffix string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + suffix
}

func hostPortToURL(value string) string {
	host, port, ok := splitHostPort(value)
	if !ok {
		return ""
	}
	if port == 443 {
		return "https://" + host
	}
	return fmt.Sprintf("https://%s:%d", host, port)
}

func splitHostPort(value string) (string, int, bool) {
	idx := strings.LastIndex(strings.TrimSpace(value), ":")
	if idx <= 0 || idx == len(value)-1 {
		return "", 0, false
	}
	port, err := strconv.Atoi(value[idx+1:])
	if err != nil || port <= 0 {
		return "", 0, false
	}
	return strings.TrimSuffix(strings.TrimSpace(value[:idx]), "."), port, true
}

func parseHostPortPort(value string) (int, bool) {
	_, port, ok := splitHostPort(value)
	return port, ok
}

func proxyTargetsPort(proxy string, localPort int) bool {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" || localPort <= 0 {
		return false
	}
	if port, err := strconv.Atoi(proxy); err == nil {
		return port == localPort
	}
	if !strings.Contains(proxy, "://") {
		proxy = "http://" + proxy
	}
	parsed, err := url.Parse(proxy)
	if err != nil {
		return false
	}
	portText := parsed.Port()
	if portText == "" {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		host = parsed.Host
	}
	if host == "" {
		return false
	}
	if host != "127.0.0.1" && !strings.EqualFold(host, "localhost") {
		return false
	}
	return port == localPort
}

func suggestedCommand(used map[int]struct{}, localPort *int) string {
	if localPort == nil || *localPort <= 0 {
		return ""
	}
	port := 443
	for _, candidate := range allowedFunnelPorts {
		if used == nil {
			port = candidate
			break
		}
		if _, exists := used[candidate]; !exists {
			port = candidate
			break
		}
	}
	return fmt.Sprintf("tailscale funnel --bg --https=%d --set-path=%s %d", port, webhookMountPath, *localPort)
}

func funnelRemediation(command string) string {
	if strings.TrimSpace(command) == "" {
		return "Set a fixed Colin `server.port`, then configure a `/webhooks` Funnel for that port."
	}
	return fmt.Sprintf("Run `%s`, then reload this page.", command)
}

func publicRemediation(command string) string {
	if strings.TrimSpace(command) == "" {
		return "Finish the local Colin and Funnel setup, then verify the public readiness URL again."
	}
	return fmt.Sprintf("Confirm Funnel is active with `%s` and that the derived public URL resolves.", command)
}

func cliError(command string, output []byte, err error) error {
	if len(strings.TrimSpace(string(output))) == 0 {
		return fmt.Errorf("%s failed: %w", command, err)
	}
	return fmt.Errorf("%s failed: %s", command, strings.TrimSpace(string(output)))
}
