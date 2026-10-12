package run

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestSSHErrorOnlySuggestsLocalNetworkForNoRoute(t *testing.T) {
	blocked := guestSSHError(context.DeadlineExceeded, fmt.Errorf("dial tcp: %w", syscall.EHOSTUNREACH))
	require.ErrorIs(t, blocked, context.DeadlineExceeded)
	require.ErrorIs(t, blocked, syscall.EHOSTUNREACH)
	require.ErrorContains(t, blocked, "Local Network access may be blocked")
	require.ErrorContains(t, blocked, "Offscreen Daemon")

	for _, other := range []error{syscall.ECONNREFUSED, errors.New("SSH authentication failed")} {
		err := guestSSHError(context.DeadlineExceeded, other)
		require.NotContains(t, err.Error(), "Local Network")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorIs(t, err, other)
	}
}
