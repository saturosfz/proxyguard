package proxyguard

import (
	"context"
	"errors"
	"io"
	"time"
)

type timeoutReader struct {
	ctx     context.Context
	reader  io.Reader
	timeout time.Duration
}

func newTimeoutReader(ctx context.Context, parent io.Reader, timeout time.Duration) *timeoutReader {
	return &timeoutReader{
		ctx:     ctx,
		reader:  parent,
		timeout: timeout,
	}
}

// ErrReaderTimeout is an error that is triggered when the TCP reader has timed out
var ErrReaderTimeout = errors.New("TCP reader timeout reached")

type retReader struct {
	n   int
	err error
}

// Read reads upon the timeout or until the default bufio reader returns
func (t *timeoutReader) Read(b []byte) (n int, err error) {
	ctx, cancel := context.WithTimeout(t.ctx, t.timeout)
	defer cancel()
	c := make(chan retReader, 1)

	go func() {
		n, err := t.reader.Read(b)
		c <- retReader{n: n, err: err}
	}()
	select {
	case <-ctx.Done():
		return 0, ErrReaderTimeout
	case got := <-c:
		return got.n, got.err
	}
}
