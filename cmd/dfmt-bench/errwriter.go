package main

import (
	"fmt"
	"io"
)

// errWriter keeps the first write error so a report that prints many lines
// can return it once instead of checking every Fprintf.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, a ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.w, format, a...)
	}
}

func (e *errWriter) println(a ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintln(e.w, a...)
	}
}
