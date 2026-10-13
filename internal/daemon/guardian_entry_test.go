package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/markphelps/offscreen/internal/run"
)

// The daemon test executable also launches host prepare through runGroup.
// Dispatch explicitly, never through a package-init side effect.
func TestMain(m *testing.M) {
	if run.MaybeGuardian(os.Args[1:]) {
		return
	}
	// Daemons reread current_image per VM job; never let a test see the user's
	// config. Tests that need a config file set their own path.
	dir, err := os.MkdirTemp("", "osd-config-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("OFFSCREEN_CONFIG", filepath.Join(dir, "config.toml"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
