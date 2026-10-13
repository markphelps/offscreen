package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"
)

// column is one field of a list command's default table.
type column struct {
	Header string
	// Field is the row struct field the column shows. A user's table template
	// that names this field gets Header as its heading.
	Field string
	// Value is the template the default table renders for the cell. Empty
	// means {{.Field}}.
	Value string
}

// ListFlags are the output flags shared by list commands, as in docker ps.
type ListFlags struct {
	Quiet  bool   `help:"Only print job IDs or image names." short:"q"`
	Format string `help:"Output format: table, json, or a Go template; 'table <template>' adds headers."`
}

// writeList prints rows on w. Quiet prints only each row's ID. Format "" or
// "table" prints the default table, "json" prints one JSON object per row,
// "table <template>" prints a headed table from the template, and any other
// value is a Go template applied to each row. Rows must be structs whose
// exported fields the templates and JSON use.
func writeList[T any](w io.Writer, rows []T, columns []column, opts ListFlags, id func(T) string) error {
	if opts.Quiet {
		for _, row := range rows {
			if _, err := fmt.Fprintln(w, id(row)); err != nil {
				return err
			}
		}
		return nil
	}
	format := opts.Format
	switch {
	case format == "json":
		enc := json.NewEncoder(w)
		for _, row := range rows {
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return nil
	case format == "" || format == "table":
		cells := make([]string, len(columns))
		headers := make([]string, len(columns))
		for i, c := range columns {
			headers[i] = c.Header
			cells[i] = c.Value
			if cells[i] == "" {
				cells[i] = "{{cell ." + c.Field + "}}"
			}
		}
		tmpl, err := parseFormat(strings.Join(cells, "\t"))
		if err != nil {
			return err
		}
		return writeTable(w, strings.Join(headers, "\t"), tmpl, rows)
	case strings.HasPrefix(format, "table ") || strings.HasPrefix(format, "table\t"):
		tmpl, err := parseFormat(strings.TrimLeft(format[len("table"):], " \t"))
		if err != nil {
			return err
		}
		headers := make(map[string]string, len(columns))
		for _, c := range columns {
			headers[c.Field] = c.Header
		}
		var header strings.Builder
		if err := tmpl.Option("missingkey=zero").Execute(&header, headers); err != nil {
			return fmt.Errorf("format: %w", err)
		}
		return writeTable(w, header.String(), tmpl, rows)
	default:
		tmpl, err := parseFormat(format)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := executeRow(w, tmpl, row); err != nil {
				return err
			}
		}
		return nil
	}
}

// parseFormat accepts \t and \n escapes so shells need not pass literal tabs,
// as docker does.
func parseFormat(text string) (*template.Template, error) {
	text = strings.NewReplacer(`\t`, "\t", `\n`, "\n").Replace(text)
	tmpl, err := template.New("format").Funcs(template.FuncMap{"cell": cell}).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("format: %w", err)
	}
	return tmpl, nil
}

func writeTable[T any](w io.Writer, header string, tmpl *template.Template, rows []T) error {
	tw := tabwriter.NewWriter(w, 0, 8, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return err
	}
	for _, row := range rows {
		if err := executeRow(tw, tmpl, row); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func executeRow(w io.Writer, tmpl *template.Template, row any) error {
	if err := tmpl.Execute(w, row); err != nil {
		return fmt.Errorf("format: %w", err)
	}
	_, err := fmt.Fprintln(w)
	return err
}

// humanSince describes how long ago t was, in docker's style ("3 hours ago").
func humanSince(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch seconds := int(d.Seconds()); {
	case seconds < 1:
		return "Less than a second ago"
	case seconds < 60:
		return plural(seconds, "second")
	}
	switch minutes := int(d.Minutes()); {
	case minutes == 1:
		return "About a minute ago"
	case minutes < 60:
		return plural(minutes, "minute")
	}
	switch hours := int(d.Round(time.Hour).Hours()); {
	case hours == 1:
		return "About an hour ago"
	case hours < 48:
		return plural(hours, "hour")
	case hours < 24*7*2:
		return plural(hours/24, "day")
	case hours < 24*30*2:
		return plural(hours/24/7, "week")
	case hours < 24*365*2:
		return plural(hours/24/30, "month")
	default:
		return plural(hours/24/365, "year")
	}
}

// cell formats v for one table cell: its first line, with tabs as spaces so
// tabwriter keeps the columns, and "…" when lines were dropped. Templates
// and JSON keep the full value.
func cell(v any) string {
	s := strings.ReplaceAll(fmt.Sprint(v), "\t", " ")
	if first, _, cut := strings.Cut(strings.TrimSpace(s), "\n"); cut {
		return strings.TrimSpace(first) + " …"
	}
	return strings.TrimSpace(s)
}
