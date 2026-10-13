package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/markphelps/offscreen/internal/tart"
)

type fakeTart struct {
	vms     []tart.VM
	listErr error
	failOn  string
	deleted []string
}

func (f *fakeTart) List(context.Context) ([]tart.VM, error) { return f.vms, f.listErr }

func (f *fakeTart) Delete(_ context.Context, name string) error {
	if name == f.failOn {
		return errors.New("tart delete failed")
	}
	f.deleted = append(f.deleted, name)
	return nil
}

const (
	current = "offscreen-20261001-xcode26.1-aaaaaaaaaaaa"
	older   = "offscreen-20260901-xcode26.0-bbbbbbbbbbbb"
	oldest  = "offscreen-20260801-xcode26-cccccccccccc"
)

func inventory() []tart.VM {
	return []tart.VM{
		{Name: current},
		{Name: older},
		{Name: oldest},
		{Name: "offscreen-20260701-xcode25.4-dddddddddddd", Running: true},
		{Name: "ghcr.io/cirruslabs/macos-tahoe-xcode:latest"},
		{Name: "offscreen-build-eeeeeeeeeeee"},
		{Name: "job-0123456789abcdef"},
		{Name: "offscreen-20260601-xcode26.0-ffffffffffff-copy"},
		{Name: "my-offscreen-20260601-xcode26.0-ffffffffffff"},
	}
}

func TestPruneDryRunSelectsOnlyStoppedNonCurrentGoldenImages(t *testing.T) {
	client := &fakeTart{vms: inventory()}
	report, err := prune(context.Background(), client, current, false, nil)
	require.NoError(t, err)
	require.Equal(t, []PruneEntry{{Name: older}, {Name: oldest}}, report.Eligible)
	require.Equal(t, []PruneEntry{{Name: "offscreen-20260701-xcode25.4-dddddddddddd", Reason: "running"}}, report.Skipped)
	require.Empty(t, report.Deleted)
	require.Empty(t, client.deleted)
}

func TestPruneApplyDeletesAndReportsFailures(t *testing.T) {
	client := &fakeTart{vms: inventory(), failOn: oldest}
	report, err := prune(context.Background(), client, current, true, nil)
	require.NoError(t, err)
	require.Equal(t, []string{older}, client.deleted)
	require.Equal(t, []PruneEntry{{Name: older}}, report.Deleted)
	require.Equal(t, []PruneEntry{{Name: oldest, Reason: "tart delete failed"}}, report.Failed)
}

func TestPruneRefusesWithoutCurrentImage(t *testing.T) {
	client := &fakeTart{vms: inventory()}
	_, err := prune(context.Background(), client, "", true, nil)
	require.ErrorContains(t, err, "no current_image")
	require.Empty(t, client.deleted)
}

func TestPruneRefusesWhenCurrentImageIsMissingFromTart(t *testing.T) {
	client := &fakeTart{vms: inventory()[1:]}
	_, err := prune(context.Background(), client, current, true, nil)
	require.ErrorContains(t, err, "not in tart list")
	require.Empty(t, client.deleted)
}

func TestPruneGuardRunsUnderLockOnlyWhenApplying(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	guardErr := errors.New("busy")
	calls := 0
	guard := func(context.Context) error {
		calls++
		// The lock is held: a second taker must block, so probe it non-blocking.
		f, err := os.Open(filepath.Join(filepath.Dir(path), "images.lock"))
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		require.ErrorIs(t, unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB), unix.EWOULDBLOCK)
		return guardErr
	}
	_, err := Prune(context.Background(), path, true, nil, guard)
	require.ErrorIs(t, err, guardErr)
	require.Equal(t, 1, calls)

	// The lock is released afterwards.
	unlock, err := lockImages(path)
	require.NoError(t, err)
	unlock()
}

func TestPruneDeletesNothingWhenTartCannotList(t *testing.T) {
	client := &fakeTart{listErr: errors.New("tart missing")}
	_, err := prune(context.Background(), client, current, true, nil)
	require.ErrorContains(t, err, "tart missing")
	require.Empty(t, client.deleted)
}

func TestPruneDeletesOnlyConfirmedImages(t *testing.T) {
	// current_image moved to a new build while the user was asked about
	// older; the new previous image (oldest here) was never shown.
	client := &fakeTart{vms: inventory()}
	report, err := prune(context.Background(), client, current, true, []string{older})
	require.NoError(t, err)
	require.Equal(t, []string{older}, client.deleted)
	require.Equal(t, []PruneEntry{{Name: older}}, report.Deleted)
	require.Contains(t, report.Skipped, PruneEntry{Name: oldest, Reason: "not in the confirmed selection"})
}
