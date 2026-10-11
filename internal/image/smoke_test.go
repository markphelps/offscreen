package image

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/guest"
	"github.com/markphelps/offscreen/internal/tart"
)

// Run explicitly with OFFSCREEN_IMAGE_SMOKE=<image name> and OFFSCREEN_CONFIG
// pointing to the isolated config used to build it. No unrelated VM is touched.
func TestImageSmoke(t *testing.T) {
	name := os.Getenv("OFFSCREEN_IMAGE_SMOKE")
	if name == "" {
		t.Skip("set OFFSCREEN_IMAGE_SMOKE to an image name")
	}
	path, err := config.Path()
	require.NoError(t, err)
	cfg, err := config.LoadFile(path)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	client := tart.Client{}
	id := "offscreen-smoke-" + time.Now().Format("150405")
	require.NoError(t, client.Clone(ctx, name, id))
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 40*time.Second)
		defer done()
		_ = client.Stop(cleanup, id)
		require.NoError(t, client.Delete(cleanup, id))
	}()
	vm, err := client.Start(ctx, id)
	require.NoError(t, err)
	connection, err := waitForGuest(ctx, client, guest.Credentials{User: cfg.Image.SSHUser, Password: cfg.Image.SSHPassword, Key: cfg.Image.SSHKey}, id)
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	out, err := guest.Run(connection, "/usr/bin/automationmodetool || true", nil)
	require.NoError(t, err)
	require.Contains(t, out, "DOES NOT REQUIRE user authentication")
	_, err = guest.Run(connection, `osascript -e 'tell application "System Events" to get name of every process'`, nil)
	require.NoError(t, err, "GUI session missing")
	out, err = guest.Run(connection, "launchctl list com.offscreen.display", nil)
	require.NoError(t, err, "display LaunchAgent not loaded")
	require.Contains(t, out, "LastExitStatus")
	display, err := parseDisplay(cfg.Image.Display)
	require.NoError(t, err)
	pixelWidth, pixelHeight := display.width, display.height
	if display.unit == "pt" {
		pixelWidth *= 2
		pixelHeight *= 2
	}
	var capture string
	for range 60 {
		capture, err = guest.Run(connection, "screencapture -x /tmp/offscreen-image-smoke.png && sips -g pixelWidth -g pixelHeight /tmp/offscreen-image-smoke.png", nil)
		if err == nil && strings.Contains(capture, fmt.Sprintf("pixelWidth: %d", pixelWidth)) && strings.Contains(capture, fmt.Sprintf("pixelHeight: %d", pixelHeight)) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	require.NoError(t, err)
	require.Contains(t, capture, fmt.Sprintf("pixelWidth: %d", pixelWidth))
	require.Contains(t, capture, fmt.Sprintf("pixelHeight: %d", pixelHeight))
	if marker := os.Getenv("OFFSCREEN_IMAGE_SMOKE_MARKER"); marker != "" {
		_, err := guest.Run(connection, "test -f "+guest.Quote(marker), nil)
		require.NoError(t, err, "setup marker %q missing", marker)
	}
	stopCtx, done := context.WithTimeout(context.Background(), 40*time.Second)
	defer done()
	require.NoError(t, client.Stop(stopCtx, id))
	require.NoError(t, vm.Wait())
}
