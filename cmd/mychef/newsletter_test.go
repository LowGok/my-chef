package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewsletterCommandRejectsMissingAuthority(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"export"}, {"erase", "--db", "missing.db", "--user-id", "1"}} {
		var output, diagnostics bytes.Buffer
		if code := runNewsletter(args, strings.NewReader(`{}`), &output, &diagnostics); code != 2 {
			t.Fatalf("invalid command returned %d", code)
		}
		if output.Len() != 0 {
			t.Fatal("invalid request emitted contact data")
		}
	}
}
