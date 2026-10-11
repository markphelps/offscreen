package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollectArtifacts(t *testing.T) {
	t.Run("active collection uses original connection", func(t *testing.T) {
		var collected, recovered bool
		err := collectArtifacts(context.Background(), func(ctx context.Context) error {
			collected = true
			return nil
		}, func(ctx context.Context) error {
			recovered = true
			return nil
		})
		require.NoError(t, err)
		require.True(t, collected)
		require.False(t, recovered)
	})

	t.Run("ordinary collection failure is not retried", func(t *testing.T) {
		original := errors.New("tar failed")
		var recovered bool
		err := collectArtifacts(context.Background(), func(ctx context.Context) error {
			return original
		}, func(ctx context.Context) error {
			recovered = true
			return nil
		})
		require.ErrorIs(t, err, original)
		require.False(t, recovered)
	})

	t.Run("expired active context uses fresh bounded recovery", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var collected, recovered bool
		err := collectArtifacts(ctx, func(ctx context.Context) error {
			collected = true
			return nil
		}, func(cleanup context.Context) error {
			recovered = true
			require.NoError(t, cleanup.Err())
			deadline, ok := cleanup.Deadline()
			require.True(t, ok)
			require.Positive(t, time.Until(deadline))
			require.LessOrEqual(t, time.Until(deadline), 30*time.Second)
			return nil
		})
		require.NoError(t, err)
		require.False(t, collected)
		require.True(t, recovered)
	})

	t.Run("cancel during collection retries and reports recovery failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		recoveryErr := errors.New("reconnect failed")
		var recovered bool
		err := collectArtifacts(ctx, func(active context.Context) error {
			cancel()
			return active.Err()
		}, func(cleanup context.Context) error {
			recovered = true
			require.NoError(t, cleanup.Err())
			return recoveryErr
		})
		require.True(t, recovered)
		require.ErrorIs(t, err, recoveryErr)
	})
}
