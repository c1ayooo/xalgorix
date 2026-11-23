package main

import (
	"strings"
	"testing"
)

func TestHostDataDirError(t *testing.T) {
	cases := []struct {
		name        string
		dataDir     string
		hostDataDir string
		inContainer bool
		wantErr     string
	}{
		{
			name:        "host binary needs no host override",
			dataDir:     "/srv/xalgorix/data",
			hostDataDir: "",
			inContainer: false,
		},
		{
			name:        "container requires the host path",
			dataDir:     "/srv/xalgorix/data",
			hostDataDir: "",
			inContainer: true,
			wantErr:     "XALGORIX_HOST_DATA_DIR is required",
		},
		{
			name:        "container accepts the same absolute path",
			dataDir:     "/srv/xalgorix/data",
			hostDataDir: "/srv/xalgorix/data",
			inContainer: true,
		},
		{
			name:        "container rejects a translated path",
			dataDir:     "/data",
			hostDataDir: "/srv/xalgorix/data",
			inContainer: true,
			wantErr:     "must equal XALGORIX_DATA_DIR",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := hostDataDirError(tc.dataDir, tc.hostDataDir, tc.inContainer)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
