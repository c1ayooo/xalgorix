package resources

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// Project container budgets.
//
// The container runtime moves tool memory out of the platform process, so the
// historical "one scan = N MB of the host" model no longer describes reality.
// These helpers keep a single place for the numbers so config, the driver and the
// dashboard all agree.
//
// Two-layer backpressure is unchanged in shape:
//  1. the project slot (this file) — how many project containers may exist;
//  2. the in-container exec slot (runtime.DockerDriver) — how many commands may
//     run inside one container.

// ProjectBudget is the per-project container budget.
type ProjectBudget struct {
	MemoryMB   int64 `json:"memory_mb"`
	CPUQuota   int64 `json:"cpu_quota"`
	CPUPeriod  int64 `json:"cpu_period"`
	Pids       int64 `json:"pids"`
	TmpfsMB    int64 `json:"tmpfs_mb"`
	DiskMB     int64 `json:"disk_mb"`
	MaxProjects int  `json:"max_projects"`
}

const defaultProjectMemMB = 2048

// CurrentProjectBudget resolves the budget from live host resources and the
// operator's overrides.
func CurrentProjectBudget() ProjectBudget {
	stats := GetStats()
	cores := maxInt(1, stats.CPUCores)

	mem := int64(envOptionalIntDefault("XALGORIX_PROJECT_MEM_MB", 0))
	if mem <= 0 {
		// Same shape as the scan budget: divide the usable RAM by the number of
		// expected concurrent projects, clamped to a sane band.
		slots := cores
		if slots < 1 {
			slots = 1
		}
		if slots > 6 {
			slots = 6
		}
		usable := stats.MemTotalMB - hostReserveMB(stats.MemTotalMB)
		if usable < 1024 {
			usable = 1024
		}
		mem = usable / int64(slots)
		if mem < 768 {
			mem = 768
		}
		if mem > 4096 {
			mem = 4096
		}
	}

	pids := int64(envOptionalIntDefault("XALGORIX_PROJECT_PIDS", 512))
	if pids < 64 {
		pids = 64
	}
	tmpfs := int64(envOptionalIntDefault("XALGORIX_PROJECT_TMPFS_MB", 512))
	disk := int64(envOptionalIntDefault("XALGORIX_PROJECT_DISK_MB", 4096))

	// Give each container one core by default; the kernel still time-slices on
	// contention, and a hard quota would make scan durations unpredictable.
	const cpuPeriod = 100000
	cpuQuota := int64(cpuPeriod)

	return ProjectBudget{
		MemoryMB:    mem,
		CPUQuota:    cpuQuota,
		CPUPeriod:   cpuPeriod,
		Pids:        pids,
		TmpfsMB:     tmpfs,
		DiskMB:      disk,
		MaxProjects: projectLimit(stats.MemTotalMB, mem),
	}
}

// projectLimit returns the configured cap or a RAM-derived one.
func projectLimit(totalMB, perProjectMB int64) int {
	if manual := envOptionalIntDefault("XALGORIX_MAX_PROJECTS", 0); manual > 0 {
		return manual
	}
	if perProjectMB <= 0 {
		return 1
	}
	usable := totalMB - hostReserveMB(totalMB)
	if usable < perProjectMB {
		return 1
	}
	limit := int(usable / perProjectMB)
	if limit < 1 {
		limit = 1
	}
	return limit
}

func envOptionalIntDefault(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		log.Printf("[RESOURCES] invalid %s=%q, using default %d", key, raw, def)
		return def
	}
	return n
}

var (
	projectSlotsOnce sync.Once
	projectSlots     chan struct{}
	projectSlotLimit int
)

// ErrProjectSlotTimeout is wrapped when a project slot could not be obtained in
// time. It is a typed throttle, not a scan failure.
var ErrProjectSlotTimeout = errors.New("project slot wait timeout")

// AcquireProjectSlot reserves capacity for one project container. The release
// function must be called exactly once when the container is removed.
//
// A slot is counted per *live container*, not per exec: an idle-but-warm project
// still costs its memory, so releasing on idle would over-admit.
func AcquireProjectSlot(ctx context.Context, maxWait time.Duration) (func(), error) {
	projectSlotsOnce.Do(func() {
		budget := CurrentProjectBudget()
		projectSlotLimit = budget.MaxProjects
		if projectSlotLimit < 1 {
			projectSlotLimit = 1
		}
		projectSlots = make(chan struct{}, projectSlotLimit)
	})
	if ctx == nil {
		ctx = context.Background()
	}
	if maxWait <= 0 {
		maxWait = DefaultToolLeaseMaxWait
	}
	deadline := time.Now().Add(maxWait)
	for {
		select {
		case projectSlots <- struct{}{}:
			return func() {
				select {
				case <-projectSlots:
				default:
				}
			}, nil
		default:
		}
		if time.Now().After(deadline) {
			return func() {}, fmt.Errorf("%w: %d/%d project containers active after %s",
				ErrProjectSlotTimeout, len(projectSlots), projectSlotLimit, maxWait)
		}
		select {
		case <-ctx.Done():
			return func() {}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ProjectSlotState reports current usage for the dashboard.
func ProjectSlotState() (active, limit int) {
	projectSlotsOnce.Do(func() {
		budget := CurrentProjectBudget()
		projectSlotLimit = budget.MaxProjects
		if projectSlotLimit < 1 {
			projectSlotLimit = 1
		}
		projectSlots = make(chan struct{}, projectSlotLimit)
	})
	return len(projectSlots), projectSlotLimit
}

// ReleaseProjectSlot is a convenience for callers that do not keep the closure.
func ReleaseProjectSlot() {
	if projectSlots == nil {
		return
	}
	select {
	case <-projectSlots:
	default:
	}
}
