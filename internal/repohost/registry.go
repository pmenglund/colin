package repohost

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnsupportedBackend       = errors.New("unsupported_repo_backend")
	ErrUnsupportedRepositoryURL = errors.New("unsupported_repository_url")
	adapters                    = map[string]Adapter{}
)

func NormalizeBackend(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return string(HostKindGitHub)
	}
	return value
}

func Lookup(kind string) (Adapter, error) {
	if adapter, ok := adapters[NormalizeBackend(kind)]; ok {
		return adapter, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedBackend, strings.TrimSpace(kind))
}

func Register(adapter Adapter) {
	if adapter == nil {
		return
	}
	kind := NormalizeBackend(string(adapter.Kind()))
	if kind == "" {
		return
	}
	adapters[kind] = adapter
}

func CurrentToken(kind string) string {
	adapter, err := Lookup(kind)
	if err != nil {
		return ""
	}
	return adapter.CurrentToken()
}
