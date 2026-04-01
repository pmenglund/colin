package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
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
	UIPort                   *int
	LocalUIBaseURL           string
	WebhookPort              *int
	LocalWebhookBaseURL      string
	ExplicitWebhookPublicURL string
}

type localAPIClient interface {
	StatusWithoutPeers(context.Context) (*ipnstate.Status, error)
	GetServeConfig(context.Context) (*ipn.ServeConfig, error)
}

// Inspector inspects local Tailscale state and Colin reachability.
type Inspector struct {
	localClient localAPIClient
	httpClient  *http.Client
	now         func() time.Time
}

// NewInspector returns the default Tailscale inspector.
func NewInspector() *Inspector {
	return &Inspector{
		localClient: &local.Client{},
		httpClient:  &http.Client{Timeout: 3 * time.Second},
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
	if i.localClient == nil {
		i.localClient = &local.Client{}
	}
	if i.now == nil {
		i.now = func() time.Time {
			return time.Now().UTC()
		}
	}
	now := i.now()
	status := domain.FunnelSetupStatus{
		GeneratedAt:         now,
		LocalBaseURL:        localUIBaseURL(opts),
		LocalWebhookBaseURL: localWebhookBaseURL(opts),
	}
	status.LocalSetupURL = joinURL(status.LocalBaseURL, setupPath)
	status.LocalReadyURL = joinURL(status.LocalWebhookBaseURL, readyzPath)
	status.SuggestedServeCommand = suggestedServeCommand(opts.UIPort)

	checks := make([]domain.SetupCheck, 0, 6)
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

	tsStatus, err := i.readStatus(ctx)
	if err != nil {
		addCheck(
			"tailscale_local_api",
			"Colin can reach the local Tailscale daemon",
			"error",
			err.Error(),
			"Start Tailscale and confirm the local Tailscale daemon is reachable.",
		)
		status.SuggestedCommand = suggestedCommand(nil, opts.WebhookPort)
		status.Checks = checks
		return finalizeStatus(status)
	}
	addCheck("tailscale_local_api", "Colin can reach the local Tailscale daemon", "ok", "Connected to the local Tailscale daemon.", "")
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
	if tsStatus.CurrentTailnet != nil && tsStatus.CurrentTailnet.MagicDNSEnabled {
		addCheck("magic_dns", "MagicDNS is enabled", "ok", "MagicDNS is enabled for the current tailnet.", "")
	} else {
		detail := "MagicDNS is disabled for the current tailnet."
		if tsStatus.CurrentTailnet == nil {
			detail = "The local Tailscale daemon is not connected to a tailnet yet."
		}
		addCheck(
			"magic_dns",
			"MagicDNS is enabled",
			"error",
			detail,
			"Enable MagicDNS in the Tailscale admin console before enabling Funnel.",
		)
	}

	serveCfg, err := i.readFunnelStatus(ctx)
	if err != nil {
		addCheck(
			"serve_route",
			"Serve proxies Colin at `/` on the tailnet",
			"error",
			err.Error(),
			serveRemediation(status.SuggestedServeCommand),
		)
		addCheck(
			"funnel_route",
			"Funnel proxies Colin at `/webhooks`",
			webhookCheckStatus(opts.WebhookPort),
			webhookCheckDetail(opts.WebhookPort, err),
			webhookCheckRemediation(opts.WebhookPort, ""),
		)
		status.SuggestedCommand = suggestedCommand(nil, opts.WebhookPort)
		status.Checks = checks
		return finalizeStatus(status)
	}

	if serveURL, err := matchServe(serveCfg, opts.UIPort); err != nil {
		addCheck(
			"serve_route",
			"Serve proxies Colin at `/` on the tailnet",
			"error",
			err.Error(),
			serveRemediation(status.SuggestedServeCommand),
		)
	} else {
		status.TailnetUIBaseURL = serveURL
		addCheck(
			"serve_route",
			"Serve proxies Colin at `/` on the tailnet",
			"ok",
			fmt.Sprintf("Detected `%s` proxying Colin from `/`.", serveURL),
			"",
		)
	}

	match, usedPorts, matchErr := matchFunnel(serveCfg, opts.WebhookPort)
	status.SuggestedCommand = suggestedCommand(usedPorts, opts.WebhookPort)
	if matchErr != nil {
		addCheck(
			"funnel_route",
			"Funnel proxies Colin at `/webhooks`",
			webhookCheckStatus(opts.WebhookPort),
			webhookCheckDetail(opts.WebhookPort, matchErr),
			webhookCheckRemediation(opts.WebhookPort, status.SuggestedCommand),
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

	if webhookConfigured(opts.WebhookPort) {
		status.Checks = append(status.Checks, probeCheck(i.httpClient, now, "local_readyz", "Colin responds locally at `/webhooks/readyz`", status.LocalReadyURL, "Start Colin so it serves the local readiness endpoint before enabling Funnel."))
		status.Checks = append(status.Checks, probeCheck(i.httpClient, now, "public_readyz", "Colin responds publicly at `/webhooks/readyz`", status.PublicReadyURL, publicRemediation(status.SuggestedCommand)))
	}
	return finalizeStatus(status)
}

// ResolveUIBaseURL returns the preferred Tailscale Serve base URL for the Colin UI when Serve proxies Colin from `/`.
func (i *Inspector) ResolveUIBaseURL(ctx context.Context, localPort *int) string {
	if localPort == nil || *localPort <= 0 {
		return ""
	}
	if i == nil {
		i = NewInspector()
	}
	if i.localClient == nil {
		i.localClient = &local.Client{}
	}
	cfg, err := i.readFunnelStatus(ctx)
	if err != nil {
		return ""
	}
	return serveUIBaseURL(cfg, *localPort)
}

type funnelMatchInfo struct {
	URL      string
	HostPort string
}

func (i *Inspector) readStatus(ctx context.Context) (*ipnstate.Status, error) {
	status, err := i.localClient.StatusWithoutPeers(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Tailscale status from LocalAPI: %w", err)
	}
	if status == nil {
		return nil, errors.New("read Tailscale status from LocalAPI: empty response")
	}
	return status, nil
}

func (i *Inspector) readFunnelStatus(ctx context.Context) (*ipn.ServeConfig, error) {
	cfg, err := i.localClient.GetServeConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Tailscale serve config from LocalAPI: %w", err)
	}
	if cfg == nil {
		return &ipn.ServeConfig{}, nil
	}
	return cfg, nil
}

func matchFunnel(cfg *ipn.ServeConfig, localPort *int) (*funnelMatchInfo, map[int]struct{}, error) {
	if localPort == nil || *localPort <= 0 {
		return nil, usedPorts(cfg), errors.New("Colin needs a fixed local `server.webhook_port` before Funnel can be configured.")
	}

	ports := usedPorts(cfg)
	var sawProxyForPort bool
	for hostPort, server := range cfg.Web {
		if server == nil {
			continue
		}
		handler, ok := server.Handlers[webhookMountPath]
		if !ok {
			for _, other := range server.Handlers {
				if other != nil && proxyTargetsPort(other.Proxy, *localPort) {
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
			URL:      hostPortToURL(string(hostPort)),
			HostPort: string(hostPort),
		}, ports, nil
	}
	if sawProxyForPort {
		return nil, ports, errors.New("Funnel points at Colin, but not from `/webhooks`.")
	}
	return nil, ports, fmt.Errorf("No Funnel proxies `127.0.0.1:%d` from `/webhooks` yet.", *localPort)
}

func usedPorts(cfg *ipn.ServeConfig) map[int]struct{} {
	out := map[int]struct{}{}
	if cfg == nil {
		return out
	}
	for hostPort := range cfg.Web {
		if port, ok := parseHostPortPort(string(hostPort)); ok {
			out[port] = struct{}{}
		}
	}
	for hostPort := range cfg.AllowFunnel {
		if port, ok := parseHostPortPort(string(hostPort)); ok {
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

func localUIBaseURL(opts Options) string {
	if value := strings.TrimSpace(opts.LocalUIBaseURL); value != "" {
		return strings.TrimRight(value, "/")
	}
	if opts.UIPort == nil || *opts.UIPort <= 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", *opts.UIPort)
}

func localWebhookBaseURL(opts Options) string {
	if value := strings.TrimSpace(opts.LocalWebhookBaseURL); value != "" {
		return strings.TrimRight(value, "/")
	}
	if opts.WebhookPort == nil || *opts.WebhookPort <= 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", *opts.WebhookPort)
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

func serveUIBaseURL(cfg *ipn.ServeConfig, localPort int) string {
	if cfg == nil || localPort <= 0 {
		return ""
	}

	type candidate struct {
		host   string
		port   int
		scheme string
	}
	matches := make([]candidate, 0, len(cfg.Web))
	for hostPort, server := range cfg.Web {
		if server == nil {
			continue
		}
		handler, ok := server.Handlers["/"]
		if !ok || handler == nil || !proxyTargetsPort(handler.Proxy, localPort) {
			continue
		}
		host, port, ok := splitHostPort(string(hostPort))
		if !ok {
			continue
		}
		scheme, ok := serveURLScheme(port)
		if !ok {
			continue
		}
		matches = append(matches, candidate{host: host, port: port, scheme: scheme})
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].scheme != matches[j].scheme {
			return matches[i].scheme == "https"
		}
		if matches[i].port != matches[j].port {
			return matches[i].port < matches[j].port
		}
		return matches[i].host < matches[j].host
	})
	if isServeDefaultPort(matches[0].scheme, matches[0].port) {
		return matches[0].scheme + "://" + matches[0].host
	}
	return fmt.Sprintf("%s://%s:%d", matches[0].scheme, matches[0].host, matches[0].port)
}

func parseHostPortPort(value string) (int, bool) {
	_, port, ok := splitHostPort(value)
	return port, ok
}

func serveURLScheme(port int) (string, bool) {
	switch port {
	case 80:
		return "http", true
	case 443, 8443, 10000:
		return "https", true
	default:
		return "", false
	}
}

func isServeDefaultPort(scheme string, port int) bool {
	return (scheme == "http" && port == 80) || (scheme == "https" && port == 443)
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

func webhookConfigured(port *int) bool {
	return port != nil && *port > 0
}

func webhookCheckStatus(port *int) string {
	if webhookConfigured(port) {
		return "error"
	}
	return "ok"
}

func webhookCheckDetail(port *int, err error) string {
	if !webhookConfigured(port) {
		return "Webhook listener is disabled because `server.webhook_port` is unset."
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func webhookCheckRemediation(port *int, command string) string {
	if !webhookConfigured(port) {
		return ""
	}
	return funnelRemediation(command)
}

func matchServe(cfg *ipn.ServeConfig, localPort *int) (string, error) {
	if localPort == nil || *localPort <= 0 {
		return "", errors.New("Colin needs a fixed local `server.port` before Serve can be configured.")
	}
	baseURL := serveUIBaseURL(cfg, *localPort)
	if strings.TrimSpace(baseURL) == "" {
		return "", fmt.Errorf("No Serve proxies `127.0.0.1:%d` from `/` yet.", *localPort)
	}
	return baseURL, nil
}

func suggestedServeCommand(localPort *int) string {
	if localPort == nil || *localPort <= 0 {
		return ""
	}
	return fmt.Sprintf("tailscale serve --bg %d", *localPort)
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
		return "Set a fixed Colin `server.webhook_port`, then configure a `/webhooks` Funnel for that port."
	}
	return fmt.Sprintf("Run `%s`, then reload this page.", command)
}

func serveRemediation(command string) string {
	if strings.TrimSpace(command) == "" {
		return "Set a fixed Colin `server.port`, then configure Tailscale Serve for that port."
	}
	return fmt.Sprintf("Run `%s`, then reload this page.", command)
}

func publicRemediation(command string) string {
	if strings.TrimSpace(command) == "" {
		return "Finish the local Colin and Funnel setup, then verify the public readiness URL again."
	}
	return fmt.Sprintf("Confirm Funnel is active with `%s` and that the derived public URL resolves.", command)
}
