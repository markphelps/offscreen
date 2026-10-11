package daemon

import (
	"os"
	"testing"

	"github.com/markphelps/offscreen/internal/run"
)

// The daemon test executable also launches host prepare through runGroup.
// Dispatch explicitly, never through a package-init side effect.
func TestMain(m *testing.M) {
	if run.MaybeGuardian(os.Args[1:]) {
		return
	}
	os.Exit(m.Run())
}
