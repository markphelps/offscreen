package run

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSlotsNeverExceedTwo(t *testing.T) {
	for _, configured := range []int{2, 4} {
		dir := t.TempDir()
		first, err := acquire(context.Background(), dir, "first", false, configured, 0)
		require.NoError(t, err)
		second, err := acquire(context.Background(), dir, "second", false, configured, 0)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err = acquire(ctx, dir, "third", false, configured, 0)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		cancel()
		first()
		third, err := acquire(context.Background(), dir, "third", false, configured, 0)
		require.NoError(t, err)
		third()
		second()
	}
}

func TestLowMemoryUsesOneSlot(t *testing.T) {
	dir := t.TempDir()
	first, err := acquire(context.Background(), dir, "first", false, 2, 1024)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = acquire(ctx, dir, "second", false, 2, 1024)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	first()
}

func TestOneSlotWaitsForOccupiedSecondSlot(t *testing.T) {
	for _, test := range []struct {
		name, project    string
		slots, threshold int
	}{
		{"memory dropped", "low-memory", 2, 1024},
		{"configured one slot", "single-slot", 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			second, err := os.OpenFile(filepath.Join(dir, "vm-1.lock"), os.O_CREATE|os.O_RDWR, 0o600)
			require.NoError(t, err)
			require.NoError(t, syscall.Flock(int(second.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
			defer func() {
				if second != nil {
					unlockFile(second)
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer cancel()
			_, err = acquire(ctx, dir, test.project, false, test.slots, test.threshold)
			require.ErrorIs(t, err, context.DeadlineExceeded, "vm-0 must stay idle while vm-1 is occupied")

			unlockFile(second)
			second = nil
			third, err := acquire(context.Background(), dir, test.project, false, test.slots, test.threshold)
			require.NoError(t, err)
			third()
		})
	}
}

func TestExclusiveProjectLock(t *testing.T) {
	dir := t.TempDir()
	first, err := acquire(context.Background(), dir, filepath.Join(dir, "same"), true, 2, 0)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = acquire(ctx, dir, filepath.Join(dir, "same"), true, 2, 0)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	other, err := acquire(context.Background(), dir, filepath.Join(dir, "other"), true, 2, 0)
	require.NoError(t, err)
	other()
	first()
}

func TestMemoryCountsReclaimablePages(t *testing.T) {
	available, err := parseVMStat(`Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free: 100.
Pages inactive: 200.
Pages speculative: 30.
Pages active: 500.
Pages wired down: 10.
`)
	require.NoError(t, err)
	require.Equal(t, uint64(330*16384), available)
}
