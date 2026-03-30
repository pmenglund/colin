package main

import (
	"bytes"
	"errors"
	"strings"
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

func TestRunHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(--help) exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "setup") {
		t.Fatalf("help output = %q, want to mention setup", got)
	}
}

func TestRunRejectsExtraRootArgs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"one", "two"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run(extra root args) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "colin [path-to-WORKFLOW.md]") {
		t.Fatalf("stderr = %q, want root usage", got)
	}
}

func TestRunRejectsSetupWithoutSubcommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run(setup) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "colin setup [command]") {
		t.Fatalf("stderr = %q, want setup help", got)
	}
}

func TestRunRejectsExtraSetupFunnelArgs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"setup", "funnel", "one", "two"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run(setup funnel extra args) exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "funnel [path-to-WORKFLOW.md]") {
		t.Fatalf("stderr = %q, want setup funnel usage", got)
	}
}
