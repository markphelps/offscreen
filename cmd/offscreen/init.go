package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/markphelps/offscreen/internal/project"
)

type InitCmd struct {
	Project string `help:"Project name. Defaults to the current directory's name."`
	Force   bool   `help:"Overwrite an existing .offscreen.toml." short:"f"`
}

func (c *InitCmd) Run() error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}
	return c.write(dir, os.Stderr)
}

// write creates the template contract in dir and prints its path and the
// next step on w. Stdout stays empty.
func (c *InitCmd) write(dir string, w io.Writer) error {
	name := c.Project
	if name == "" {
		name = filepath.Base(dir)
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("init: project name cannot be empty; pass --project")
	}
	contents, err := contractTemplate(name)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, project.FileName)
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if c.Force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644) // #nosec G302 -- a repo file meant to be committed and shared
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(contents); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "wrote %s\nedit it for what your project needs, then run: offscreen check\n", path)
	return err
}

// contractTemplate returns a contract with only project set. Every other
// section is commented out, so the file is valid with no tasks.
func contractTemplate(name string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(templateHeader)
	if err := toml.NewEncoder(&buf).Encode(map[string]string{"project": name}); err != nil {
		return nil, fmt.Errorf("encode project name: %w", err)
	}
	buf.WriteString(templateBody)
	return buf.Bytes(), nil
}

const templateHeader = `# Offscreen project contract. See
# https://github.com/markphelps/offscreen/blob/main/docs/config.md
#
# Uncomment and edit the sections your project needs, then run
# "offscreen check" to validate this file without starting a VM.

# Must match the <project> in "offscreen run <project> ...".
`

const templateBody = `
# Run this project's jobs one at a time, across both lanes.
# exclusive = true

# Runs on your Mac in the project root before each job, with your
# environment. Use it to build what the job needs.
# [prepare]
# run = "make build"
# Files or directories to copy into the VM after prepare, such as a
# built app. The checkout itself is copied separately.
# share = ["build/MyApp.app"]

# Runs inside each VM from the copied project root, before the task.
# The default image has Xcode but no Homebrew, mise or project packages.
# [guest]
# setup = "sh scripts/setup-guest.sh"

# A named task: offscreen run <project> <name>.
#   lane = "vm"    runs in a throwaway VM (use this unless the task needs
#                  real hardware)
#   lane = "host"  runs on your real desktop when you are away
#   run            a shell command, run from the project root
# Every task gets OFFSCREEN_OUT (write screenshots and results here),
# OFFSCREEN_JOB (job ID), OFFSCREEN_LANE (vm or host) and OFFSCREEN_CACHE
# (a cache that persists across this project's jobs).
# [tasks.example]
# lane = "vm"
# run = "sh scripts/example.sh"
`
