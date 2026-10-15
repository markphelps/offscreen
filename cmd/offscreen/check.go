package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"text/tabwriter"

	"github.com/markphelps/offscreen/internal/project"
)

type CheckCmd struct {
	Worktree string `help:"Path to a git worktree." type:"path"`
}

// Run validates the contract without contacting the daemon. An invalid or
// missing contract is returned, so report prints it and exits 1.
func (c *CheckCmd) Run() error {
	contract, err := project.Find("", c.Worktree)
	if err != nil {
		return err
	}
	return printContract(contract, os.Stdout)
}

// printContract prints the contract's path, project and tasks with lanes.
func printContract(contract project.Contract, w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s: ok\nproject: %s\n", filepath.Join(contract.Root, project.FileName), contract.Project); err != nil {
		return err
	}
	if len(contract.Tasks) == 0 {
		_, err := fmt.Fprintf(w, "tasks: none; add a [tasks.<name>] table, or run one-off commands with: offscreen run %s -- <cmd>\n", contract.Project)
		return err
	}
	if _, err := fmt.Fprintln(w, "tasks:"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, name := range slices.Sorted(maps.Keys(contract.Tasks)) {
		if _, err := fmt.Fprintf(tw, "  %s\t%s\n", name, contract.Tasks[name].Lane); err != nil {
			return err
		}
	}
	return tw.Flush()
}
