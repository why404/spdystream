package spdystream

import (
	"code.google.com/p/go.net/spdy"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type Stream struct {
	streamId  spdy.StreamId
	parent    *Stream
	conn      *Connection
	startChan chan error

	dataLock sync.RWMutex
	dataChan chan []byte
	unread   []byte

	headers    http.Header
	headerChan chan http.Header
	finishLock sync.Mutex
	replied    bool
	finished   bool
	closeChan  chan bool
}

// WriteData writes data to stream, sending a dataframe per call
func (s *Stream) WriteData(data []byte, fin bool) error {
	var flags spdy.DataFlags

	if fin {
		flags = spdy.DataFlagFin
		s.finishLock.Lock()
		if s.finished {
			s.finishLock.Unlock()
			return ErrWriteClosedStream
		}
		s.finished = true
		s.finishLock.Unlock()
	}

	dataFrame := &spdy.DataFrame{
		StreamId: s.streamId,
		Flags:    flags,
		Data:     data,
	}

	return s.conn.framer.WriteFrame(dataFrame)
}

// Write writes bytes to a stream, calling write data for each call.
func (s *Stream) Write(data []byte) (n int, err error) {
	err = s.WriteData(data, false)
	if err == nil {
		n = len(data)
	}
	return
}

// Read reads bytes from a stream, a single read will never get more
// than what is sent on a single data frame, but a multiple calls to
// read may get data from the same data frame.
func (s *Stream) Read(p []byte) (n int, err error) {
	if s.unread == nil {
		select {
		case <-s.closeChan:
			return 0, io.EOF
		case read, ok := <-s.dataChan:
			if !ok {
				return 0, io.EOF
			}
			s.unread = read
		}
	}
	n = copy(p, s.unread)
	if n < len(s.unread) {
		s.unread = s.unread[n:]
	} else {
		s.unread = nil
	}
	return
}

// Wait waits for the stream to receive a reply.
func (s *Stream) Wait() error {
	return s.WaitTimeout(time.Duration(0))
}

// WaitTimeout waits for the stream to receive a reply or for timeout.
// When the timeout is reached, ErrTimeout will be returned.
func (s *Stream) WaitTimeout(timeout time.Duration) error {
	var timeoutChan <-chan time.Time
	if timeout > time.Duration(0) {
		timeoutChan = time.After(timeout)
	}

	select {
	case err := <-s.startChan:
		if err != nil {
			return err
		}
		break
	case <-timeoutChan:
		return ErrTimeout
	}
	return nil
}

// Close closes the stream by sending a reset stream frame, indicating
// this side is finished with the stream and the other side should close.
func (s *Stream) Close() error {
	s.conn.removeStream(s)
	s.finishLock.Lock()
	if s.finished {
		s.finishLock.Unlock()
		return nil
	}
	s.finished = true
	s.finishLock.Unlock()
	resetFrame := &spdy.RstStreamFrame{
		StreamId: s.streamId,
		Status:   spdy.Cancel,
	}
	writeErr := s.conn.framer.WriteFrame(resetFrame)
	if writeErr != nil {
		return writeErr
	}
	return nil
}

// CreateSubStream creates a stream using the current as the parent
func (s *Stream) CreateSubStream(headers http.Header, fin bool) (*Stream, error) {
	return s.conn.CreateStream(headers, s, fin)
}

// SendHeader sends a header frame across the stream
func (s *Stream) SendHeader(headers http.Header, fin bool) error {
	return s.conn.sendHeaders(headers, s, fin)
}

// ReceiveHeader receives a header sent on the other side
// of the stream.  This function will block until a header
// is received or stream is closed.
func (s *Stream) ReceiveHeader() (http.Header, error) {
	select {
	case <-s.closeChan:
		break
	case header, ok := <-s.headerChan:
		if !ok {
			return nil, fmt.Errorf("header chan closed")
		}
		return header, nil
	}
	return nil, fmt.Errorf("stream closed")
}

// Parent returns the parent stream
func (s *Stream) Parent() *Stream {
	return s.parent
}

// Headers returns the headers used to create the stream
func (s *Stream) Headers() http.Header {
	return s.headers
}

// String returns the string version of stream using the
// streamId to uniquely identify the stream
func (s *Stream) String() string {
	return fmt.Sprintf("stream:%d", s.streamId)
}

// IsFinished returns whether the stream has finished
// sending data
func (s *Stream) IsFinished() bool {
	return s.finished
}
