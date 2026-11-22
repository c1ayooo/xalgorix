// Package coldpath composes the optional PostgreSQL InfoItem replica, River
// range jobs, and the journal reconciler. The hot path never waits for it.
package coldpath

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// Applier is implemented by the PostgreSQL InfoItem store.
type Applier interface {
	Apply(ctx context.Context, rec domainjournal.Record) error
}

// Processor applies one journal range to the cold path.
type Processor struct {
	root  string
	store Applier
}

func NewProcessor(root string, store Applier) (*Processor, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("coldpath: data root is required")
	}
	if store == nil {
		return nil, errors.New("coldpath: applier is required")
	}
	return &Processor{root: filepath.Clean(root), store: store}, nil
}

func (p *Processor) ProcessRange(ctx context.Context, scanID, scanDir string, fromSeq, toSeq uint64) error {
	if fromSeq == 0 || toSeq < fromSeq {
		return fmt.Errorf("coldpath: invalid range %d..%d", fromSeq, toSeq)
	}
	path, err := p.safeJournalPath(scanDir)
	if err != nil {
		return err
	}
	// R6.1 (charter §6.3): the journal's durable source is the PG primary
	// store once the backend is registered — the file path is only the
	// legacy fallback (pre-R6 directories whose import has not produced
	// rows). A backend read error surfaces instead of silently reading a
	// stale file (D12 red line).
	records, _, err := domainjournal.ReadRecordsResolved(ctx, path, scanID)
	if err != nil {
		return err
	}
	bySeq := make(map[uint64]domainjournal.Record, len(records))
	for _, rec := range records {
		if rec.ScanID != scanID {
			return fmt.Errorf("coldpath: journal contains scan %q, expected %q", rec.ScanID, scanID)
		}
		if rec.Seq >= fromSeq && rec.Seq <= toSeq {
			bySeq[rec.Seq] = rec
		}
	}
	for seq := fromSeq; seq <= toSeq; seq++ {
		rec, ok := bySeq[seq]
		if !ok {
			return fmt.Errorf("coldpath: journal %s is missing seq %d", path, seq)
		}
		if err := p.store.Apply(ctx, rec); err != nil {
			return fmt.Errorf("coldpath: apply %s seq %d: %w", scanID, seq, err)
		}
	}
	return nil
}

func (p *Processor) safeJournalPath(scanDir string) (string, error) {
	if p == nil || strings.TrimSpace(p.root) == "" {
		return "", errors.New("coldpath: processor unavailable")
	}
	if scanDir == "" || filepath.IsAbs(scanDir) {
		return "", fmt.Errorf("coldpath: scan_dir must be relative, got %q", scanDir)
	}
	dir := filepath.Join(p.root, filepath.Clean(scanDir))
	rel, err := filepath.Rel(p.root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("coldpath: scan_dir escapes data root: %q", scanDir)
	}
	return filepath.Join(dir, domainjournal.FileName), nil
}
