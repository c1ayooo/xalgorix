package web

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

// H-4 regression: concurrent localized reports must write into the directory
// supplied by their own caller, never a shared Server.currentScanDir.
func TestGenerateReportUsesCallerScanDirConcurrently(t *testing.T) {
	s := &Server{
		dataDir: t.TempDir(),
		cfg:     &config.Config{Language: "zh-CN"},
	}
	dirs := []string{t.TempDir(), t.TempDir()}
	records := []*ScanRecord{
		{ID: "concurrent-a", Target: "a.example", Status: "finished"},
		{ID: "concurrent-b", Target: "b.example", Status: "finished"},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(dirs))
	for i := range dirs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.generateReport(records[i], dirs[i])
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
		want := filepath.Join(dirs[i], "xalgorix_report_"+records[i].ID+".pdf")
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("report %d not in its own scan dir %s: %v", i, want, err)
		}
	}
}
