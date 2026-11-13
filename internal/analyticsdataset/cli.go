package analyticsdataset

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"time"
)

func parseDate(value string) (int64, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, ErrInput
}

// Run is the normal CLI entry point, also exercised with disposable files in tests.
func Run(ctx context.Context, args []string, out, diagnostics io.Writer) (result error) {
	defer func() {
		if result != nil {
			_, _ = fmt.Fprintln(diagnostics, "Analytics operation failed. Check the command and synthetic dataset contract.")
		}
	}()
	if len(args) == 0 {
		return ErrInput
	}
	op := args[0]
	allowed := map[string][]string{
		"fixture": {"db", "manifest", "as-of"}, "import": {"db", "manifest"}, "validate": {"db"},
		"report": {"db", "start", "end", "dimension", "format", "monthly"}, "expire": {"db"}, "delete-subject": {"db", "subject"},
	}
	flags, ok := allowed[op]
	if !ok {
		return ErrInput
	}
	fs := flag.NewFlagSet("mychef-analytics", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("db", "", "analytical database path")
	manifest := fs.String("manifest", "", "synthetic JSON manifest")
	asof := fs.String("as-of", DefaultReportingDate, "frozen fixture reporting date")
	start := fs.String("start", "", "report start, inclusive")
	end := fs.String("end", "", "report end, exclusive")
	dimension := fs.String("dimension", "all", "single demographic dimension")
	format := fs.String("format", "json", "json or text")
	monthly := fs.Bool("monthly", false, "monthly contribution history")
	subject := fs.String("subject", "", "opaque subject to erase")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *path == "" {
		return ErrInput
	}
	valid := true
	fs.Visit(func(f *flag.Flag) {
		if !slices.Contains(flags, f.Name) {
			valid = false
		}
	})
	if !valid {
		return ErrInput
	}
	if op == "fixture" {
		r, err := parseDate(*asof)
		if err != nil {
			return err
		}
		b, err := BuildFixture(r)
		if err != nil {
			return err
		}
		if err = createImported(ctx, *path, b); err != nil {
			return err
		}
		if *manifest != "" {
			f, err := os.OpenFile(*manifest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				_ = os.Remove(*path)
				return ErrInput
			}
			err = json.NewEncoder(f).Encode(b)
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				_ = os.Remove(*manifest)
				_ = os.Remove(*path)
				return ErrInput
			}
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": "created", "environment": "synthetic", "population": 1000, "reporting_unix": r})
	}
	if op == "import" {
		if *manifest == "" {
			return ErrInput
		}
		f, err := os.Open(*manifest)
		if err != nil {
			return ErrInput
		}
		b, err := Decode(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		if _, err = os.Lstat(*path); os.IsNotExist(err) {
			if err = createImported(ctx, *path, b); err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(map[string]any{"status": "imported"})
		}
		d, err := Open(ctx, *path, false)
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		added, err := d.Import(ctx, b)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": "valid", "imported": added})
	}
	d, err := Open(ctx, *path, op == "report" || op == "validate")
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	switch op {
	case "validate":
		return json.NewEncoder(out).Encode(map[string]any{"status": "valid", "environment": "synthetic", "schema_version": 1, "reporting_unix": d.reportingUnix})
	case "expire":
		if err := d.Expire(ctx); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": "expired", "reporting_unix": d.reportingUnix})
	case "delete-subject":
		if err := d.DeleteSubject(ctx, *subject); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": "deleted"})
	case "report":
		if !slices.Contains([]string{"json", "text"}, *format) {
			return ErrInput
		}
		if *monthly {
			if *start != "" || *end != "" {
				return ErrInput
			}
			r, err := d.Monthly(ctx, *dimension)
			if err != nil {
				return err
			}
			if *format == "text" {
				if _, err := fmt.Fprintf(out, "synthetic extract %s reporting %s months %s through %s dimension %s schema 1 query 1\n", r.ExtractID, time.Unix(r.ReportingUnix, 0).UTC().Format(time.RFC3339), r.StartMonth, r.EndMonth, r.Dimension); err != nil {
					return err
				}
				for _, m := range r.Rows {
					if _, err := fmt.Fprintf(out, "%s %s\n", m.Month, metricText(m.Metric)); err != nil {
						return err
					}
				}
				return nil
			}
			return json.NewEncoder(out).Encode(r)
		}
		until := monday(time.Unix(d.reportingUnix, 0)).Unix()
		from := until - 7*86400
		if *start != "" {
			from, err = parseDate(*start)
			if err != nil {
				return err
			}
		}
		if *end != "" {
			until, err = parseDate(*end)
			if err != nil {
				return err
			}
		}
		r, err := d.Report(ctx, ReportOptions{Start: from, End: until, Dimension: *dimension})
		if err != nil {
			return err
		}
		if *format == "text" {
			if _, err := fmt.Fprintf(out, "synthetic extract %s reporting %s period %s through %s dimension %s schema 1 query 1\n", r.ExtractID, time.Unix(r.ReportingUnix, 0).UTC().Format(time.RFC3339), time.Unix(r.Start, 0).UTC().Format(time.RFC3339), time.Unix(r.End, 0).UTC().Format(time.RFC3339), r.Dimension); err != nil {
				return err
			}
			for _, source := range r.Coverage {
				if _, err := fmt.Fprintf(out, "source %s %s\n", source.Source, source.Status); err != nil {
					return err
				}
			}
			for _, m := range r.Rows {
				if _, err := fmt.Fprintln(out, metricText(m)); err != nil {
					return err
				}
			}
			return nil
		}
		return json.NewEncoder(out).Encode(r)
	}
	return ErrInput
}

func createImported(ctx context.Context, path string, b Batch) error {
	d, err := Create(ctx, path, b.ExtractID, b.ReportingUnix)
	if err != nil {
		return err
	}
	_, err = d.Import(ctx, b)
	closeErr := d.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		if err != nil {
			return err
		}
		return ErrDataset
	}
	return nil
}

func metricText(m Metric) string {
	label := m.Name
	if m.SurveyVersion != "" {
		label += " " + m.SurveyVersion
	}
	value := ""
	if m.Value != nil && m.Denominator != nil {
		value = fmt.Sprintf(" %d/%d", *m.Value, *m.Denominator)
	}
	return fmt.Sprintf("%s %s %s%s", label, m.Segment, m.Status, value)
}
