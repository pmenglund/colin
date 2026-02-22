package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pmenglund/colin/internal/linear"
)

var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*m`)

type metadataLookupStub struct {
	issue linear.Issue
	err   error
}

func (s metadataLookupStub) GetIssueByIdentifier(_ context.Context, _ string) (linear.Issue, error) {
	if s.err != nil {
		return linear.Issue{}, s.err
	}
	return s.issue, nil
}

func TestMetadataCommandHelp(t *testing.T) {
	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"metadata", "--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Display Colin metadata for a Linear issue") {
		t.Fatalf("metadata help output missing description: %q", out)
	}
}

func TestRunMetadataWithLookupPrintsSortedMetadata(t *testing.T) {
	var out bytes.Buffer
	err := runMetadataWithLookup(context.Background(), &out, metadataLookupStub{
		issue: linear.Issue{
			Identifier: "COLIN-42",
			Metadata: map[string]string{
				"colin.worktree_path": "/tmp/worktree",
				"colin.branch_name":   "colin/COLIN-42",
			},
		},
	}, "COLIN-42")
	if err != nil {
		t.Fatalf("runMetadataWithLookup() error = %v", err)
	}

	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("expected ANSI color codes in output, got %q", out.String())
	}

	want := "" +
		"Issue: COLIN-42\n" +
		"Metadata:\n" +
		"colin.branch_name=colin/COLIN-42\n" +
		"colin.worktree_path=/tmp/worktree\n"
	if stripANSI(out.String()) != want {
		t.Fatalf("output = %q, want %q", stripANSI(out.String()), want)
	}
}

func TestRunMetadataWithLookupPrintsEmptyMetadata(t *testing.T) {
	var out bytes.Buffer
	err := runMetadataWithLookup(context.Background(), &out, metadataLookupStub{
		issue: linear.Issue{
			Identifier: "COLIN-42",
			Metadata:   map[string]string{},
		},
	}, "COLIN-42")
	if err != nil {
		t.Fatalf("runMetadataWithLookup() error = %v", err)
	}

	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("expected ANSI color codes in output, got %q", out.String())
	}

	want := "" +
		"Issue: COLIN-42\n" +
		"Metadata: (empty)\n"
	if stripANSI(out.String()) != want {
		t.Fatalf("output = %q, want %q", stripANSI(out.String()), want)
	}
}

func TestRunMetadataWithLookupRequiresIdentifier(t *testing.T) {
	err := runMetadataWithLookup(context.Background(), &bytes.Buffer{}, metadataLookupStub{}, "   ")
	if err == nil {
		t.Fatal("expected error for empty identifier")
	}
	if !strings.Contains(err.Error(), "issue identifier is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunMetadataWithLookupReturnsLookupError(t *testing.T) {
	wantErr := errors.New("lookup failed")
	err := runMetadataWithLookup(context.Background(), &bytes.Buffer{}, metadataLookupStub{err: wantErr}, "COLIN-42")
	if err == nil {
		t.Fatal("expected lookup error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestMetadataCommandUsesFakeBackendConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "colin.toml")
	content := `linear_backend = "fake"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("LINEAR_API_TOKEN", "")
	t.Setenv("LINEAR_TEAM_ID", "")

	rootCmd := NewRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--config", configPath, "metadata", "COL-FAKE-1"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	out := buf.String()
	plainOut := stripANSI(out)
	if !strings.Contains(plainOut, "Issue: COL-FAKE-1") {
		t.Fatalf("output missing issue header: %q", out)
	}
	if !strings.Contains(plainOut, "Metadata: (empty)") {
		t.Fatalf("output missing empty metadata marker: %q", out)
	}
}

func stripANSI(text string) string {
	return ansiRegexp.ReplaceAllString(text, "")
}
