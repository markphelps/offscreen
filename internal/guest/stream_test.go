package guest

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandKeepsArgvBoundariesAndMetacharacters(t *testing.T) {
	args := []string{"/bin/sh", "-c", `printf '<%s>\n' "$@"`, "sh", "a b", `quote'and"double`, "$HOME; exit 8", ""}
	command := Command("", args)
	output, err := exec.Command("/bin/bash", "-c", command).Output()
	require.NoError(t, err)
	require.Equal(t, "<a b>\n<quote'and\"double>\n<$HOME; exit 8>\n<>\n", string(output))
}

func TestCommandRetainsShellScript(t *testing.T) {
	output, err := exec.Command("/bin/bash", "-c", Command(`printf '%s' 'shell ok'`, nil)).Output() // #nosec G204 -- constant test fixture
	require.NoError(t, err)
	require.Equal(t, "shell ok", string(output))
}
