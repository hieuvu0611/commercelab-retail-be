package config

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestApplyLowResourceLimits(t *testing.T) {
	ApplyLowResourceLimits()

	if got := runtime.GOMAXPROCS(0); got != DefaultMaxProcs {
		t.Fatalf("expected GOMAXPROCS=%d for <=0.25 CPU budget, got %d", DefaultMaxProcs, got)
	}

	if got := debug.SetMemoryLimit(-1); got != DefaultSoftMemoryLimitBytes {
		t.Fatalf("expected GOMEMLIMIT=%d bytes (220 MiB) for <=250MB RAM budget, got %d", DefaultSoftMemoryLimitBytes, got)
	}
}
