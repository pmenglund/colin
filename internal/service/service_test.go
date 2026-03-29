package service

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewLoggerSuppressesInfoWhenNotVerbose(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := newLogger(&output, false)

	logger.Info("hidden")
	logger.Error("visible")

	got := output.String()
	if strings.Contains(got, "hidden") {
		t.Fatalf("logger output = %q, unexpected info log", got)
	}
	if !strings.Contains(got, "visible") {
		t.Fatalf("logger output = %q, missing error log", got)
	}
}

func TestNewLoggerIncludesInfoWhenVerbose(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := newLogger(&output, true)

	logger.Info("visible")

	if got := output.String(); !strings.Contains(got, "visible") {
		t.Fatalf("logger output = %q, missing info log", got)
	}
}
