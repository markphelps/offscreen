package main

import (
	"cmp"
	"context"
	"io"
	"os"
	"slices"
	"time"

	"github.com/markphelps/offscreen/internal/daemon"
)

type StatusCmd struct {
	All       bool `help:"Include completed jobs." short:"a"`
	ListFlags `embed:""`
}

// jobRow is what status prints for a job: the table cells, the template
// fields, and the JSON keys.
type jobRow struct {
	ID        string
	Project   string
	Task      string
	Lane      string
	State     string
	Created   string
	CreatedAt time.Time
	Reason    string
}

var jobColumns = []column{
	{Header: "JOB ID", Field: "ID"},
	{Header: "PROJECT", Field: "Project"},
	{Header: "TASK", Field: "Task"},
	{Header: "LANE", Field: "Lane"},
	{Header: "STATE", Field: "State"},
	{Header: "CREATED", Field: "Created"},
	{Header: "REASON", Field: "Reason"},
}

func (c *StatusCmd) Run() error {
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	jobs, err := client.Jobs(context.Background())
	if err != nil {
		return err
	}
	return c.print(os.Stdout, jobs, time.Now())
}

// print lists queued and active jobs, or every job with All, newest first.
func (c *StatusCmd) print(w io.Writer, jobs []daemon.Job, now time.Time) error {
	rows := make([]jobRow, 0, len(jobs))
	for _, j := range jobs {
		if !c.All && daemon.Terminal(j.State) {
			continue
		}
		rows = append(rows, jobRow{ID: j.ID, Project: j.Project, Task: j.Task, Lane: string(j.Lane), State: j.State, Created: humanSince(now, j.Created), CreatedAt: j.Created, Reason: j.Reason})
	}
	slices.SortStableFunc(rows, func(a, b jobRow) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return writeList(w, rows, jobColumns, c.ListFlags, func(r jobRow) string { return r.ID })
}
