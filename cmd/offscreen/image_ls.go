package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/image"
)

type ImageLsCmd struct {
	ListFlags `embed:""`
}

// imageRow is what image ls prints for a golden image: the table cells, the
// template fields, and the JSON keys.
type imageRow struct {
	Name    string
	Current bool
	Running bool
	// Created is the build date in the image name; images record no time.
	Created string
	Size    string
	SizeGB  int
}

var imageColumns = []column{
	{Header: "NAME", Field: "Name"},
	{Header: "CURRENT", Field: "Current", Value: "{{if .Current}}*{{end}}"},
	{Header: "CREATED", Field: "Created"},
	{Header: "SIZE", Field: "Size"},
}

func (c *ImageLsCmd) Run() error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	inv, err := image.List(context.Background(), path)
	if err != nil {
		return err
	}
	if inv.CurrentMissing {
		slog.Warn("current_image is not in tart list; run offscreen image build", "current_image", inv.Current)
	}
	return c.print(os.Stdout, inv)
}

func (c *ImageLsCmd) print(w io.Writer, inv image.Inventory) error {
	rows := make([]imageRow, 0, len(inv.Images))
	for _, g := range inv.Images {
		row := imageRow{Name: g.Name, Current: g.Current, Running: g.Running, SizeGB: g.SizeGB}
		if !g.Created.IsZero() {
			row.Created = g.Created.Format(time.DateOnly)
		}
		if g.SizeGB > 0 {
			row.Size = fmt.Sprintf("%d GB", g.SizeGB)
		}
		rows = append(rows, row)
	}
	return writeList(w, rows, imageColumns, c.ListFlags, func(r imageRow) string { return r.Name })
}
