package main

import (
	"bytes"
	"log/slog"
	"sync"
)

// maxLine is the most bytes of one record; a longer line is logged in parts.
const maxLine = 64 << 10

// lineLog logs each line written to it as one record of source. A line without its newline waits for the next
// write or for Flush, up to maxLine bytes. Safe for concurrent use.
type lineLog struct {
	log    *slog.Logger
	source string

	mu  sync.Mutex
	buf []byte
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.buf = append(l.buf, p...)

	for {
		i := bytes.IndexByte(l.buf, '\n')

		switch {
		case i >= 0 && i <= maxLine:
			l.emit(l.buf[:i])
			l.buf = l.buf[i+1:]
		case len(l.buf) >= maxLine:
			l.emit(l.buf[:maxLine])
			l.buf = l.buf[maxLine:]
		default:
			return len(p), nil
		}
	}
}

// Flush logs the unterminated last line, if any.
func (l *lineLog) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.buf) > 0 {
		l.emit(l.buf)
		l.buf = nil
	}
}

func (l *lineLog) emit(line []byte) {
	l.log.Info("output", "source", l.source, "line", string(line))
}
