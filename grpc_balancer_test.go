package pinpoint

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// fakeBalancerCC stands in for the channel: it hands out fakeSubConns and
// records every balancer state the policy publishes. The embedded interface is
// nil, so any method the policy is not expected to call panics loudly.
type fakeBalancerCC struct {
	balancer.ClientConn
	mu       sync.Mutex
	subConns []*fakeSubConn
	states   []balancer.State
}

func (cc *fakeBalancerCC) NewSubConn(_ []resolver.Address, opts balancer.NewSubConnOptions) (balancer.SubConn, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	sc := &fakeSubConn{listener: opts.StateListener}
	cc.subConns = append(cc.subConns, sc)
	return sc, nil
}

func (cc *fakeBalancerCC) UpdateState(s balancer.State) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.states = append(cc.states, s)
}

func (cc *fakeBalancerCC) count() int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return len(cc.subConns)
}

func (cc *fakeBalancerCC) subConn(i int) *fakeSubConn {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.subConns[i]
}

func (cc *fakeBalancerCC) state() balancer.State {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.states[len(cc.states)-1]
}

// pick runs the current picker and returns the SubConn it chose.
func (cc *fakeBalancerCC) pick() (balancer.SubConn, error) {
	res, err := cc.state().Picker.Pick(balancer.PickInfo{})
	return res.SubConn, err
}

// fakeSubConn records Connect and Shutdown; the test drives its state through
// the listener the policy registered, as the channel's serializer would.
type fakeSubConn struct {
	balancer.SubConn
	listener  func(balancer.SubConnState)
	connects  atomic.Int32
	shutdowns atomic.Int32
}

func (sc *fakeSubConn) Connect()  { sc.connects.Add(1) }
func (sc *fakeSubConn) Shutdown() { sc.shutdowns.Add(1) }

func (sc *fakeSubConn) setState(s connectivity.State) {
	sc.listener(balancer.SubConnState{ConnectivityState: s})
}

func newExpiringPickFirst(t *testing.T, maxAgeMillis int64) (*expiringPickFirst, *fakeBalancerCC) {
	t.Helper()
	cc := &fakeBalancerCC{}
	b := expiringPickFirstBuilder{}.Build(cc, balancer.BuildOptions{}).(*expiringPickFirst)
	cfg, err := expiringPickFirstBuilder{}.ParseConfig([]byte(`{"maxAgeMillis":` + itoa(maxAgeMillis) + `}`))
	require.NoError(t, err)
	require.NoError(t, b.UpdateClientConnState(balancer.ClientConnState{
		ResolverState:  resolver.State{Addresses: []resolver.Address{{Addr: "collector:9991"}}},
		BalancerConfig: cfg,
	}))
	return b, cc
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// readyExpiringPickFirst brings the first SubConn to READY and lets it pass
// its max age.
func readyExpiringPickFirst(t *testing.T, maxAgeMillis int64) (*expiringPickFirst, *fakeBalancerCC) {
	t.Helper()
	b, cc := newExpiringPickFirst(t, maxAgeMillis)
	require.Equal(t, 1, cc.count(), "the first address update opens one SubConn")
	require.EqualValues(t, 1, cc.subConn(0).connects.Load())
	cc.subConn(0).setState(connectivity.Connecting)
	cc.subConn(0).setState(connectivity.Ready)
	require.Equal(t, connectivity.Ready, cc.state().ConnectivityState)
	time.Sleep(time.Duration(maxAgeMillis) * 2 * time.Millisecond) // past the jittered max age
	return b, cc
}

// Acceptance criterion 4: however often Pick runs on an expired SubConn,
// exactly one successor is created for it. Run with -race: Pick runs on the
// RPC goroutines while the successor is created on another.
func Test_expiringPickFirst_createsExactlyOneSuccessorUnderConcurrentPicks(t *testing.T) {
	_, cc := readyExpiringPickFirst(t, 1)

	var wg sync.WaitGroup
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				sc, err := cc.pick()
				assert.NoError(t, err)
				assert.Same(t, cc.subConn(0), sc, "the expired SubConn keeps serving until its successor is READY")
			}
		}()
	}
	wg.Wait()

	waitFor(t, "the successor to be created", func() bool { return cc.count() == 2 })
	time.Sleep(20 * time.Millisecond) // give any duplicate a chance to show up
	assert.Equal(t, 2, cc.count(), "100k picks on one expired SubConn open exactly one successor")
	assert.EqualValues(t, 1, cc.subConn(1).connects.Load())
	assert.EqualValues(t, 0, cc.subConn(0).shutdowns.Load(), "make-before-break: the old SubConn is not shut down yet")
}

// Acceptance criterion 2: the switch happens only once the successor is READY,
// and the old SubConn is shut down gracefully at that moment.
func Test_expiringPickFirst_makeBeforeBreak(t *testing.T) {
	_, cc := readyExpiringPickFirst(t, 1)
	cc.pick()
	waitFor(t, "the successor to be created", func() bool { return cc.count() == 2 })
	old, successor := cc.subConn(0), cc.subConn(1)

	successor.setState(connectivity.Connecting)
	sc, err := cc.pick()
	require.NoError(t, err)
	assert.Same(t, old, sc, "a CONNECTING successor does not affect picks")
	assert.Equal(t, connectivity.Ready, cc.state().ConnectivityState)

	successor.setState(connectivity.Ready)
	sc, err = cc.pick()
	require.NoError(t, err)
	assert.Same(t, successor, sc, "the READY successor takes over")
	assert.EqualValues(t, 1, old.shutdowns.Load(), "the replaced SubConn is shut down once")
	assert.EqualValues(t, 0, successor.shutdowns.Load())
}

// Acceptance criterion 3: a successor that cannot connect leaves the old
// SubConn serving, and the channel stays READY.
func Test_expiringPickFirst_keepsOldSubConnWhenSuccessorFails(t *testing.T) {
	_, cc := readyExpiringPickFirst(t, 1)
	cc.pick()
	waitFor(t, "the successor to be created", func() bool { return cc.count() == 2 })
	old, successor := cc.subConn(0), cc.subConn(1)

	successor.setState(connectivity.Connecting)
	successor.setState(connectivity.TransientFailure)
	sc, err := cc.pick()
	require.NoError(t, err)
	assert.Same(t, old, sc)
	assert.Equal(t, connectivity.Ready, cc.state().ConnectivityState)
	assert.EqualValues(t, 0, old.shutdowns.Load())

	// grpc-go returns a failed SubConn to IDLE after its back-off; the policy
	// asks it to try again, and a further failure does not pile up SubConns.
	successor.setState(connectivity.Idle)
	assert.EqualValues(t, 2, successor.connects.Load(), "IDLE after a failure is reconnected")
	successor.setState(connectivity.Connecting)
	successor.setState(connectivity.TransientFailure)
	assert.Equal(t, 2, cc.count(), "the old SubConn's successor is requested once, not per failure")
	assert.Same(t, old, must(cc.pick()))
}

// The single-SubConn-per-slot invariant: while the successor holds the
// CONNECTING slot, an old SubConn that drops and wants to reconnect is shut
// down rather than becoming a second connection attempt.
func Test_expiringPickFirst_oneSubConnPerSlot(t *testing.T) {
	_, cc := readyExpiringPickFirst(t, 1)
	cc.pick()
	waitFor(t, "the successor to be created", func() bool { return cc.count() == 2 })
	old, successor := cc.subConn(0), cc.subConn(1)
	successor.setState(connectivity.Connecting)

	old.setState(connectivity.Idle)
	assert.EqualValues(t, 1, old.shutdowns.Load(), "the CONNECTING slot is taken, so the dropped SubConn is shut down")
	assert.EqualValues(t, 1, old.connects.Load(), "and not reconnected")
	assert.Equal(t, connectivity.Connecting, cc.state().ConnectivityState)
	_, err := cc.pick()
	assert.ErrorIs(t, err, balancer.ErrNoSubConnAvailable, "RPCs queue until the successor is READY")

	successor.setState(connectivity.Ready)
	assert.Same(t, successor, must(cc.pick()))
	assert.Equal(t, 2, cc.count())
}

// Acceptance criterion 1: with the max age off, a SubConn is never replaced no
// matter how old it gets or how often it is picked.
func Test_expiringPickFirst_disabledNeverCreatesSuccessor(t *testing.T) {
	_, cc := readyExpiringPickFirst(t, 0)
	for i := 0; i < 1000; i++ {
		assert.Same(t, cc.subConn(0), must(cc.pick()))
	}
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 1, cc.count())
}

// A successor requested just before Close must not open a SubConn on a
// balancer the channel has already torn down.
func Test_expiringPickFirst_closeStopsPendingSuccessor(t *testing.T) {
	b, cc := readyExpiringPickFirst(t, 1)
	sd := b.ready
	require.True(t, sd.successor.CompareAndSwap(false, true), "claim the successor as a pick would")

	b.Close()
	assert.EqualValues(t, 1, cc.subConn(0).shutdowns.Load())
	b.requestSuccessor(sd)
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 1, cc.count(), "no SubConn after Close")
}

// A READY SubConn whose connection drops is reconnected, and while it does the
// channel reports CONNECTING so the agent's readiness waits hold.
func Test_expiringPickFirst_reconnectsDroppedReadySubConn(t *testing.T) {
	_, cc := readyExpiringPickFirst(t, 0)
	sc := cc.subConn(0)

	sc.setState(connectivity.Idle)
	assert.EqualValues(t, 2, sc.connects.Load())
	assert.Equal(t, connectivity.Connecting, cc.state().ConnectivityState)
	sc.setState(connectivity.Connecting)
	sc.setState(connectivity.Ready)
	assert.Equal(t, connectivity.Ready, cc.state().ConnectivityState)
	assert.Equal(t, 1, cc.count())
}

func Test_expiringPickFirst_exitIdleConnectsOnlyWhenNothingIsUp(t *testing.T) {
	b, cc := newExpiringPickFirst(t, 0)
	b.ExitIdle()
	assert.Equal(t, 1, cc.count(), "a CONNECTING SubConn is left to finish")
	cc.subConn(0).setState(connectivity.Ready)
	b.ExitIdle()
	assert.Equal(t, 1, cc.count(), "a READY SubConn needs no connection attempt")
}

func Test_expiringPickFirst_parseConfig(t *testing.T) {
	cfg, err := expiringPickFirstBuilder{}.ParseConfig([]byte(`{"maxAgeMillis":60000,"unknown":1}`))
	require.NoError(t, err)
	assert.EqualValues(t, 60000, cfg.(expiringPickFirstConfig).MaxAgeMillis)

	_, err = expiringPickFirstBuilder{}.ParseConfig([]byte(`{"maxAgeMillis":"soon"}`))
	assert.Error(t, err)

	assert.Equal(t, `{"loadBalancingConfig":[{"subconnection_expiring_pick_first":{"maxAgeMillis":60000}}]}`,
		expiringPickFirstServiceConfig(time.Minute))
}

// connCounter counts the connections a server accepts.
type connCounter struct{ conns atomic.Int32 }

func (*connCounter) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context   { return ctx }
func (*connCounter) HandleRPC(context.Context, stats.RPCStats)                         {}
func (*connCounter) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }
func (c *connCounter) HandleConn(_ context.Context, s stats.ConnStats) {
	if _, ok := s.(*stats.ConnBegin); ok {
		c.conns.Add(1)
	}
}

// The policy on a real channel and transport: with a 50ms max age, a second of
// steady RPCs rotates the connection several times, and because the switch is
// make-before-break not one RPC sees Unavailable. The server has no services,
// so every RPC that reaches it ends in Unimplemented.
func Test_expiringPickFirst_rotatesConnectionsWithoutFailingRpcs(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	counter := &connCounter{}
	srv := grpc.NewServer(grpc.StatsHandler(counter))
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.Dial(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(expiringPickFirstServiceConfig(50*time.Millisecond)))
	require.NoError(t, err)
	defer conn.Close()

	calls := 0
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); calls++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := conn.Invoke(ctx, "/pinpoint.test/Ping", &emptypb.Empty{}, &emptypb.Empty{})
		cancel()
		require.Equal(t, codes.Unimplemented, status.Code(err), "call %d: %v", calls, err)
		time.Sleep(5 * time.Millisecond)
	}

	conns := counter.conns.Load()
	assert.GreaterOrEqual(t, conns, int32(3), "the connection is rotated while traffic flows")
	assert.LessOrEqual(t, conns, int32(30), "%d calls must not open a connection per call", calls)
}

// With the policy left unselected the channel behaves as before: one
// connection for as long as it lives.
func Test_expiringPickFirst_notSelectedByDefault(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	counter := &connCounter{}
	srv := grpc.NewServer(grpc.StatsHandler(counter))
	go srv.Serve(lis)
	defer srv.Stop()

	cfg, err := NewConfig(WithAppName("TestApp"))
	require.NoError(t, err)
	conn, err := grpc.Dial(lis.Addr().String(), newGrpcChannelOptions(cfg).dialOptions(insecure.NewCredentials())...)
	require.NoError(t, err)
	defer conn.Close()

	for deadline := time.Now().Add(200 * time.Millisecond); time.Now().Before(deadline); {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := conn.Invoke(ctx, "/pinpoint.test/Ping", &emptypb.Empty{}, &emptypb.Empty{})
		cancel()
		require.Equal(t, codes.Unimplemented, status.Code(err))
		time.Sleep(5 * time.Millisecond)
	}
	assert.EqualValues(t, 1, counter.conns.Load())
}

func must(sc balancer.SubConn, err error) balancer.SubConn {
	if err != nil {
		panic(err)
	}
	return sc
}
