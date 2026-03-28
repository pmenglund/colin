package config

import "sync"

// Provider keeps a last-known-good runtime config loaded from current sources.
type Provider struct {
	opts    LoadOptions
	mu      sync.RWMutex
	current Config
}

// NewProvider loads the initial config and returns a reloadable provider.
func NewProvider(opts LoadOptions) (*Provider, error) {
	cfg, err := LoadWithOptions(opts)
	if err != nil {
		return nil, err
	}
	return &Provider{
		opts:    opts,
		current: cfg,
	}, nil
}

// Current returns the last successfully loaded config snapshot.
func (p *Provider) Current() Config {
	if p == nil {
		return Config{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.current
}

// Reload re-reads configuration and only updates Current on success.
func (p *Provider) Reload() error {
	if p == nil {
		return nil
	}
	cfg, err := LoadWithOptions(p.opts)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.current = cfg
	p.mu.Unlock()
	return nil
}
