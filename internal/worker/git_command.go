package worker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func gitRun(ctx context.Context, gitBinary string, args ...string) error {
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func gitOutput(ctx context.Context, gitBinary string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func gitOutputAllowMissing(ctx context.Context, gitBinary string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil
	}
	return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
}

func gitCheckExitCodeOneMeansFalse(ctx context.Context, gitBinary string, args ...string) (bool, error) {
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
}
