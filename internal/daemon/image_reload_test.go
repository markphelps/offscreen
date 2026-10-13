package daemon

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/run"
)

// image build runs after the daemon starts (install, then build); VM jobs
// must use the image it records without a daemon restart.
func TestVMJobsUseImageBuiltAfterStartup(t *testing.T) {
	images := make(chan string, 1)
	_, client, root := fixture(t, func(_ context.Context, o run.Options) run.Result {
		images <- o.Config.CurrentImage
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	path := os.Getenv("OFFSCREEN_CONFIG")
	require.NoError(t, config.SetCurrentImageFile(path, "offscreen-new"))
	submitFixture(t, client, root, Request{Detach: true})
	require.Equal(t, "offscreen-new", <-images)
}
