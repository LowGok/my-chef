package analytics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analyticsdataset"
)

func TestWorkerHeartbeatAndDailyExtract(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if e := f.s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	path, e := f.s.Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	*f.now = f.now.Add(time.Minute)
	if e = f.s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	same, e := f.s.Current(ctx)
	if e != nil || same != path {
		t.Fatal("more than one automatic extract in the day", e)
	}
	*f.now = f.now.Add(24 * time.Hour)
	if e = f.s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	next, e := f.s.Current(ctx)
	if e != nil || next == path {
		t.Fatal("daily generation missing", e)
	}
	var output bytes.Buffer
	if e = f.s.WithReport(ctx, next, f.now.Add(-60*time.Second).Unix(), f.now.Unix()+1, &output); e != nil {
		t.Fatal(e)
	}
	var r analyticsdataset.Report
	if e = json.Unmarshal(output.Bytes(), &r); e != nil {
		t.Fatal(e)
	}
	for _, m := range r.Rows {
		if m.Name == "opens" && m.Status != "unavailable" {
			t.Fatal("deferred opens source became available", m.Status)
		}
	}
}
