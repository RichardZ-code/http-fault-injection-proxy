package proxy

import (
	"context"
	"errors"
	"net"
	"sync"
)

type connectionKey struct{}

// net/http closes the connection on a chunk-writer error. Its background read
// then cancels the request, which is not independent evidence of client abort.
// Record native I/O causality before that close can erase the distinction.
type requestConnection struct {
	net.Conn
	mu                            sync.Mutex
	writeFailed, clientReadFailed bool
	readTimedOut                  bool
	closeOnce                     sync.Once
	closeErr                      error
	onClose                       func(error)
}

func (c *requestConnection) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		if c.closeErr != nil && !onlyExpected(c.closeErr, net.ErrClosed) && c.onClose != nil {
			c.onClose(c.closeErr)
		}
	})
	return c.closeErr
}

func (c *requestConnection) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil {
		c.mu.Lock()
		c.writeFailed = true
		c.mu.Unlock()
	}
	return n, err
}

func (c *requestConnection) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		c.mu.Lock()
		c.readTimedOut = true
		c.mu.Unlock()
	}
	if err != nil && !(errors.As(err, &timeout) && timeout.Timeout()) {
		c.mu.Lock()
		if !c.writeFailed {
			c.clientReadFailed = true
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *requestConnection) inputTimedOut() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readTimedOut
}
func (c *requestConnection) resetInputTimeout() { c.mu.Lock(); c.readTimedOut = false; c.mu.Unlock() }

func clientCancellation(ctx context.Context) bool {
	if ctx.Err() == nil {
		return false
	}
	if c, ok := ctx.Value(connectionKey{}).(*requestConnection); ok {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.clientReadFailed || !c.writeFailed
	}
	return true
}
