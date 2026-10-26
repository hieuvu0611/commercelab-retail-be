package config

import (
	"os"
	"runtime"
	"runtime/debug"
)

const (
	// DefaultSoftMemoryLimitBytes = 220 MiB (leaves ~30MB headroom for OS/runtime under hard 250MB container cap)
	DefaultSoftMemoryLimitBytes int64 = 220 * 1024 * 1024
	// DefaultMaxProcs = 1 (prevents OS thread thrashing when container is throttled to <= 0.25 CPU)
	DefaultMaxProcs int = 1
)

func init() {
	ApplyLowResourceLimits()
}

// ApplyLowResourceLimits tunes the Go runtime for ultra-low resource containers (<= 250MB RAM, <= 0.25 CPU).
// Without this, Go reads the host's CPU count (e.g., 8-12 cores) and spawns too many OS threads that thrash
// under Linux CFS quota (0.25 CPU), or allows heap spikes to trigger the container OOM killer at 250MB.
func ApplyLowResourceLimits() {
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(DefaultMaxProcs)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(DefaultSoftMemoryLimitBytes)
	}
}

func EnvOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
