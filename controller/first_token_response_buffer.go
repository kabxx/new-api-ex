package controller

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

var errFirstTokenResponseDiscarded = errors.New("first-token attempt response discarded")

// firstTokenResponseBuffer keeps an attempt's headers and body private until
// the upstream has produced a valid first output. This is intentionally an
// HTTP writer wrapper rather than provider-specific output plumbing: it also
// covers adapters that use c.Stream, direct writes, or custom SSE scanners.
type firstTokenResponseBuffer struct {
	gin.ResponseWriter
	mu           sync.Mutex
	downstreamMu sync.Mutex
	buffer       bytes.Buffer
	header       http.Header
	status       int
	committed    bool
	discarded    bool
	wroteHeader  bool
	flushPending bool
	maxBytes     int
	onOverflow   func()
	onWriteError func(error)
	releaseErr   error
}

func newFirstTokenResponseBuffer(underlying gin.ResponseWriter, maxBytes int, onOverflow func(), onWriteError func(error)) *firstTokenResponseBuffer {
	header := make(http.Header)
	for key, values := range underlying.Header() {
		header[key] = append([]string(nil), values...)
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return &firstTokenResponseBuffer{
		ResponseWriter: underlying,
		header:         header,
		status:         http.StatusOK,
		maxBytes:       maxBytes,
		onOverflow:     onOverflow,
		onWriteError:   onWriteError,
	}
}

func (w *firstTokenResponseBuffer) Header() http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.header
}

func (w *firstTokenResponseBuffer) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.discarded || w.committed || w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
}

func (w *firstTokenResponseBuffer) WriteHeaderNow() {
	w.WriteHeader(w.status)
}

func (w *firstTokenResponseBuffer) Write(data []byte) (int, error) {
	w.mu.Lock()
	if w.discarded {
		w.mu.Unlock()
		return 0, errFirstTokenResponseDiscarded
	}
	if w.committed {
		underlying := w.ResponseWriter
		w.mu.Unlock()
		w.downstreamMu.Lock()
		defer w.downstreamMu.Unlock()
		written, err := underlying.Write(data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
		w.recordWriteError(err)
		return written, err
	}
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	if w.buffer.Len()+len(data) > w.maxBytes {
		w.discarded = true
		onOverflow := w.onOverflow
		w.mu.Unlock()
		if onOverflow != nil {
			onOverflow()
		}
		return 0, errFirstTokenResponseDiscarded
	}
	_, _ = w.buffer.Write(data)
	w.mu.Unlock()
	return len(data), nil
}

func (w *firstTokenResponseBuffer) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *firstTokenResponseBuffer) Flush() {
	w.mu.Lock()
	if w.discarded {
		w.mu.Unlock()
		return
	}
	if !w.committed {
		w.flushPending = true
		w.mu.Unlock()
		return
	}
	underlying := w.ResponseWriter
	w.mu.Unlock()
	w.downstreamMu.Lock()
	defer w.downstreamMu.Unlock()
	if flusher, ok := underlying.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *firstTokenResponseBuffer) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return w.ResponseWriter.Status()
	}
	return w.status
}

func (w *firstTokenResponseBuffer) Size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return w.ResponseWriter.Size()
	}
	return w.buffer.Len()
}

func (w *firstTokenResponseBuffer) Written() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.committed || w.wroteHeader || w.buffer.Len() > 0
}

// Unwrap lets http.ResponseController reach the real connection for write
// deadlines while the response body remains buffered.
func (w *firstTokenResponseBuffer) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *firstTokenResponseBuffer) Release() error {
	w.downstreamMu.Lock()
	defer w.downstreamMu.Unlock()
	w.mu.Lock()
	if w.discarded || w.committed {
		err := w.releaseErr
		w.mu.Unlock()
		return err
	}
	w.committed = true
	status := w.status
	wroteHeader := w.wroteHeader
	body := append([]byte(nil), w.buffer.Bytes()...)
	flushPending := w.flushPending
	w.buffer.Reset()
	underlying := w.ResponseWriter
	header := make(http.Header, len(w.header))
	for key, values := range w.header {
		header[key] = append([]string(nil), values...)
	}
	w.mu.Unlock()

	copyHeader(underlying.Header(), header)
	if wroteHeader {
		underlying.WriteHeader(status)
	}
	if len(body) > 0 {
		written, err := underlying.Write(body)
		if err == nil && written != len(body) {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.mu.Lock()
			w.releaseErr = err
			w.mu.Unlock()
			w.recordWriteError(err)
			return err
		}
	}
	if flushPending {
		if flusher, ok := underlying.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return nil
}

func (w *firstTokenResponseBuffer) recordWriteError(err error) {
	if err == nil || w.onWriteError == nil {
		return
	}
	w.onWriteError(err)
}

func (w *firstTokenResponseBuffer) Discard() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return
	}
	w.discarded = true
	w.buffer.Reset()
}

func copyHeader(dst, src http.Header) {
	for key := range dst {
		delete(dst, key)
	}
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
}
