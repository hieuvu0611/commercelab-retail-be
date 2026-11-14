package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryStartup(t *testing.T) {
	boom := errors.New("not ready")
	tests := []struct {
		name      string
		failFirst int
		attempts  int
		wantErr   bool
		wantCalls int
	}{
		{name: "succeeds on first try", failFirst: 0, attempts: 3, wantErr: false, wantCalls: 1},
		{name: "succeeds after transient failures", failFirst: 2, attempts: 3, wantErr: false, wantCalls: 3},
		{name: "gives up after all attempts", failFirst: 5, attempts: 3, wantErr: true, wantCalls: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := retryStartup(context.Background(), tc.attempts, time.Millisecond, func() error {
				calls++
				if calls <= tc.failFirst {
					return boom
				}
				return nil
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if calls != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestRetryStartupStopsWhenContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryStartup(ctx, 5, time.Hour, func() error {
		calls++
		return errors.New("down")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
