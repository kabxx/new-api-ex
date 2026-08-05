package common

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingEventWriter struct {
	header http.Header
}

func (writer *failingEventWriter) Header() http.Header {
	return writer.header
}

func (writer *failingEventWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic write failure")
}

func (writer *failingEventWriter) WriteHeader(int) {}

func TestCustomEventRenderReturnsWriterError(t *testing.T) {
	writer := &failingEventWriter{header: make(http.Header)}

	err := (CustomEvent{Data: "data: payload"}).Render(writer)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "synthetic write failure")
}
