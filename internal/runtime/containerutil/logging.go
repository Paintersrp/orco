package containerutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/Paintersrp/orco/internal/runtime"
)

type LogWriter struct {
	ctx    context.Context
	emitFn func(runtime.LogEntry)
	source string
	level  string
	buf    bytes.Buffer
	mu     sync.Mutex
}

func NewLogWriter(ctx context.Context, emit func(runtime.LogEntry), source, level string) *LogWriter {
	return &LogWriter{ctx: ctx, emitFn: emit, source: source, level: level}
}

func (w *LogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := len(p)
	reader := bufio.NewReader(bytes.NewReader(p))
	for {
		segment, err := reader.ReadBytes('\n')
		if len(segment) > 0 {
			if segment[len(segment)-1] == '\n' {
				w.buf.Write(segment[:len(segment)-1])
				w.emit(w.buf.String())
				w.buf.Reset()
			} else {
				w.buf.Write(segment)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return total, err
		}
	}
	return total, nil
}

func (w *LogWriter) emit(line string) {
	if line == "" {
		return
	}
	select {
	case <-w.ctx.Done():
		return
	default:
	}
	w.emitFn(runtime.LogEntry{Message: line, Source: w.source, Level: w.level})
}

func (w *LogWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() == 0 {
		return
	}
	w.emit(w.buf.String())
	w.buf.Reset()
}
