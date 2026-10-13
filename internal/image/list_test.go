package image

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/tart"
)

func TestListReturnsGoldenImagesNewestFirstAndMarksCurrent(t *testing.T) {
	vms := inventory()
	vms[0].Size = 73
	inv, err := list(context.Background(), &fakeTart{vms: vms}, current)
	require.NoError(t, err)
	require.False(t, inv.CurrentMissing)
	var names []string
	for _, g := range inv.Images {
		names = append(names, g.Name)
	}
	require.Equal(t, []string{current, older, oldest, "offscreen-20260701-xcode25.4-dddddddddddd"}, names)
	require.True(t, inv.Images[0].Current)
	require.Equal(t, 73, inv.Images[0].SizeGB)
	require.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), inv.Images[0].Created)
	require.False(t, inv.Images[1].Current)
	require.True(t, inv.Images[3].Running)
}

func TestListIncludesHandSetCurrentImage(t *testing.T) {
	vms := append(inventory(), tart.VM{Name: "my-golden"})
	inv, err := list(context.Background(), &fakeTart{vms: vms}, "my-golden")
	require.NoError(t, err)
	require.False(t, inv.CurrentMissing)
	last := inv.Images[len(inv.Images)-1]
	require.Equal(t, Golden{Name: "my-golden", Current: true}, last)
}

func TestListReportsMissingCurrentImage(t *testing.T) {
	inv, err := list(context.Background(), &fakeTart{vms: inventory()[1:]}, current)
	require.NoError(t, err)
	require.True(t, inv.CurrentMissing)
	for _, g := range inv.Images {
		require.False(t, g.Current)
	}
}

func TestListWithoutCurrentImage(t *testing.T) {
	inv, err := list(context.Background(), &fakeTart{vms: inventory()}, "")
	require.NoError(t, err)
	require.False(t, inv.CurrentMissing)
	require.Len(t, inv.Images, 4)
}

func TestListPropagatesTartErrors(t *testing.T) {
	_, err := list(context.Background(), &fakeTart{listErr: errors.New("tart list failed")}, current)
	require.ErrorContains(t, err, "tart list failed")
}
