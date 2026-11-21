//go:build !linux

package egress

import (
	"context"
	"os/exec"
)

func preparePlatform(_ context.Context, scanID string) (Status, error) {
	return Status{}, enforceUnavailable(scanID, ErrNotSupported)
}

func forgetPlatform(string) {}

// ApplyChildCredential is unavailable outside Linux. enforce mode already
// fails closed during PrepareScan, so this is never reached in a running scan.
func ApplyChildCredential(*exec.Cmd, string) error {
	return ErrNotSupported
}

// PrepareChildAccess is a no-op outside Linux: enforce mode fails closed in
// PrepareScan before any child can be launched.
func PrepareChildAccess(string, string, ...string) error { return nil }
