package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestPrintMainError(t *testing.T) {
	var buf bytes.Buffer

	if err := printMainError(&buf, errors.New("boom")); err != nil {
		t.Fatalf("printMainError() error = %v", err)
	}

	const want = "\x1b[31mboom\x1b[0m\n"
	if got := buf.String(); got != want {
		t.Fatalf("printMainError() output = %q, want %q", got, want)
	}
}
