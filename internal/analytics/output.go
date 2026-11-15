package analytics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// ContextOutput is implemented by the HTTP boundary, whose network write deadline
// must stop a pending write when ctx expires. Ordinary arbitrary callbacks are
// intentionally not an output boundary.
type ContextOutput interface {
	WriteContext(context.Context, []byte) (int, error)
}

type OutputWriter struct {
	Context context.Context
	Output  io.Writer
}

func (w OutputWriter) Write(b []byte) (int, error) { return writeOutput(w.Context, w.Output, b) }

func writeOutput(ctx context.Context, output io.Writer, b []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var n int
	var err error
	switch w := output.(type) {
	case *bytes.Buffer:
		n, err = w.Write(b)
	case *strings.Builder:
		n, err = w.Write(b)
	case ContextOutput:
		n, err = w.WriteContext(ctx, b)
	case *os.File:
		info, e := w.Stat()
		if e != nil {
			return 0, e
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			return 0, ErrPrivacy
		}
		e = w.SetWriteDeadline(deadline)

		if e != nil && !info.Mode().IsRegular() {
			// Inherited stdout is often a blocking descriptor without Go's poller.
			// Reopen the same pipe/terminal with its own nonblocking file description;
			// changing the inherited descriptor's flags would affect the caller too.
			duplicate, openErr := os.OpenFile(fmt.Sprintf("/proc/self/fd/%d", w.Fd()), os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if openErr != nil {
				return 0, ErrPrivacy
			}
			defer func() { _ = duplicate.Close() }()
			actual, statErr := duplicate.Stat()
			if statErr != nil || !os.SameFile(info, actual) {
				return 0, ErrPrivacy
			}
			w = duplicate
			e = w.SetWriteDeadline(deadline)
			if e != nil {
				return 0, ErrPrivacy
			}
		}
		if e == nil {
			done := make(chan struct{})
			stop := context.AfterFunc(ctx, func() { _ = w.SetWriteDeadline(time.Now()); close(done) })
			defer func() {
				if !stop() {
					<-done
				}
				_ = w.SetWriteDeadline(time.Time{})
			}()
		}
		n, err = w.Write(b)
	default:
		return 0, ErrInput
	}
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return n, errors.Join(err, ctx.Err())
}
