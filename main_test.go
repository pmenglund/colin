package main

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnnounceStartupPrintsDashboardURL(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var url atomic.Pointer[string]
	runErrCh := make(chan error, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		value := "http://127.0.0.1:9999"
		url.Store(&value)
	}()

	exited, err := announceStartup(&stdout, true, func() string {
		current := url.Load()
		if current == nil {
			return ""
		}
		return *current
	}, func() string {
		current := url.Load()
		if current == nil {
			return ""
		}
		return *current + "/setup/funnel"
	}, runErrCh)
	if exited {
		t.Fatal("announceStartup() reported service exit before startup announcement")
	}
	if err != nil {
		t.Fatalf("announceStartup() error = %v", err)
	}

	got := stdout.String()
	want := "Colin is running. Web UI: http://127.0.0.1:9999 Setup: http://127.0.0.1:9999/setup/funnel\n"
	if got != want {
		t.Fatalf("startup output = %q, want %q", got, want)
	}
}

func TestAnnounceStartupReturnsRunErrorBeforeDashboardReady(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	runErrCh := make(chan error, 1)
	wantErr := errors.New("boom")
	runErrCh <- wantErr

	exited, err := announceStartup(&stdout, true, func() string { return "" }, func() string { return "" }, runErrCh)
	if !exited {
		t.Fatal("announceStartup() reported startup announcement instead of service exit")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("announceStartup() error = %v, want %v", err, wantErr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("startup output = %q, want empty", stdout.String())
	}
}

func TestAnnounceStartupWithoutDashboardPrintsRunningMessage(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	runErrCh := make(chan error)

	exited, err := announceStartup(&stdout, false, func() string { return "" }, func() string { return "" }, runErrCh)
	if exited {
		t.Fatal("announceStartup() reported service exit for dashboard-disabled service")
	}
	if err != nil {
		t.Fatalf("announceStartup() error = %v", err)
	}
	if got, want := stdout.String(), "Colin is running.\n"; got != want {
		t.Fatalf("startup output = %q, want %q", got, want)
	}
}
