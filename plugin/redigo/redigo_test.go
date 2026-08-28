package ppredigo

import (
	"context"
	"errors"
	"testing"

	"github.com/gomodule/redigo/redis"
)

// fakeRedisConn implements only the base redis.Conn interface - no
// ConnWithTimeout, no ConnWithContext.
type fakeRedisConn struct {
	recvCh chan struct{}
}

var _ redis.Conn = (*fakeRedisConn)(nil)

func (f *fakeRedisConn) Close() error { return nil }
func (f *fakeRedisConn) Err() error   { return nil }
func (f *fakeRedisConn) Do(cmd string, args ...interface{}) (interface{}, error) {
	return nil, nil
}
func (f *fakeRedisConn) Send(cmd string, args ...interface{}) error { return nil }
func (f *fakeRedisConn) Flush() error                               { return nil }
func (f *fakeRedisConn) Receive() (interface{}, error) {
	<-f.recvCh
	return nil, nil
}

// redigo supports one goroutine in Send/Flush concurrent with another blocked
// in Receive (pub/sub); the wrapper must not corrupt or race in that pattern.
// Run under -race.
func Test_wrappedConn_ConcurrentSendAndReceive(t *testing.T) {
	fake := &fakeRedisConn{recvCh: make(chan struct{})}
	c := wrapConn(fake, "localhost")

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := c.Receive(); err != nil {
			t.Errorf("Receive() = %v", err)
		}
	}()

	for i := 0; i < 100; i++ {
		WithContext(c, context.Background())
		if err := c.Send("PING"); err != nil {
			t.Fatalf("Send() = %v", err)
		}
	}
	close(fake.recvCh)
	<-done
}

// A base connection without the optional interfaces must yield redigo's own
// errors instead of a nil-interface panic.
func Test_wrappedConn_MissingOptionalInterfaces(t *testing.T) {
	c := wrapConn(&fakeRedisConn{}, "localhost").(*wrappedConn)

	if _, err := c.DoWithTimeout(0, "PING"); !errors.Is(err, errTimeoutNotSupported) {
		t.Errorf("DoWithTimeout() = %v, want errTimeoutNotSupported", err)
	}
	if _, err := c.ReceiveWithTimeout(0); !errors.Is(err, errTimeoutNotSupported) {
		t.Errorf("ReceiveWithTimeout() = %v, want errTimeoutNotSupported", err)
	}
	if _, err := c.DoContext(context.Background(), "PING"); !errors.Is(err, errContextNotSupported) {
		t.Errorf("DoContext() = %v, want errContextNotSupported", err)
	}
	if _, err := c.ReceiveContext(context.Background()); !errors.Is(err, errContextNotSupported) {
		t.Errorf("ReceiveContext() = %v, want errContextNotSupported", err)
	}
}
