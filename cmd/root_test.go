package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommandDefaultRun(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if got := buf.String(); got != "colin running\n" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestRootCommandVerboseFlag(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--verbose"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if got := buf.String(); got != "colin running (verbose mode)\n" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestRootCommandHelpIncludesVerboseFlag(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	helpOutput := buf.String()
	if !strings.Contains(helpOutput, "--verbose") {
		t.Fatalf("expected help output to contain --verbose, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "--config") {
		t.Fatalf("expected help output to contain --config, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "worker") {
		t.Fatalf("expected help output to contain worker command, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "setup") {
		t.Fatalf("expected help output to contain setup command, got: %q", helpOutput)
	}
	if !strings.Contains(helpOutput, "metadata") {
		t.Fatalf("expected help output to contain metadata command, got: %q", helpOutput)
	}
}
