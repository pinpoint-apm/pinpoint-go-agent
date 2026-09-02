package pinpoint

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

// expiringPickFirstName is the load balancing policy that rotates the collector
// connection while traffic flows, selected by connectCollector only when
// Collector.Grpc.ConnectionMaxAge is set. It is the Go port of the Java agent's
// SubconnectionExpiringLoadBalancer (policy name and behavior alike).
//
// pick_first keeps one connection for the life of the channel, so an agent
// behind an L4 load balancer or a scaled-out collector stays pinned to the
// backend it first reached. This policy keeps at most one SubConn per state
// (READY, CONNECTING, TRANSIENT_FAILURE) and, once the READY SubConn is older
// than its max age, the next pick creates a successor. The successor replaces
// the old SubConn only when it becomes READY (make-before-break); if it never
// does, the old one keeps serving.
const expiringPickFirstName = "subconnection_expiring_pick_first"

func init() {
	balancer.Register(expiringPickFirstBuilder{})
}

// expiringPickFirstConfig is the policy's service config, produced by
// ParseConfig from the JSON connectCollector passes through
// grpc.WithDefaultServiceConfig.
type expiringPickFirstConfig struct {
	serviceconfig.LoadBalancingConfig `json:"-"`
	MaxAgeMillis                      int64 `json:"maxAgeMillis,omitempty"`
}

// expiringPickFirstServiceConfig renders the default service config that
// selects this policy with the given max age.
func expiringPickFirstServiceConfig(maxAge time.Duration) string {
	return fmt.Sprintf(`{"loadBalancingConfig":[{%q:{"maxAgeMillis":%d}}]}`, expiringPickFirstName, maxAge.Milliseconds())
}

type expiringPickFirstBuilder struct{}

func (expiringPickFirstBuilder) Name() string { return expiringPickFirstName }

func (expiringPickFirstBuilder) Build(cc balancer.ClientConn, _ balancer.BuildOptions) balancer.Balancer {
	return &expiringPickFirst{cc: cc}
}

func (expiringPickFirstBuilder) ParseConfig(js json.RawMessage) (serviceconfig.LoadBalancingConfig, error) {
	var cfg expiringPickFirstConfig
	if err := json.Unmarshal(js, &cfg); err != nil {
		return nil, fmt.Errorf("%s: unable to unmarshal LB policy config %s: %w", expiringPickFirstName, string(js), err)
	}
	return cfg, nil
}

// expiringPickFirst holds at most one SubConn per slot. grpc-go serializes the
// Balancer callbacks (UpdateClientConnState, ResolverError, ExitIdle, Close)
// and the SubConn StateListeners on one goroutine, but Picker.Pick runs on the
// RPC goroutines and the successor it requests is created on a goroutine of
// its own, so every field below is guarded by mu.
type expiringPickFirst struct {
	cc balancer.ClientConn

	mu     sync.Mutex
	closed bool
	maxAge time.Duration
	addrs  []resolver.Address

	ready      *expiringSubConn
	connecting *expiringSubConn
	failure    *expiringSubConn
	failureErr error
}

// expiringSubConn is one SubConn with the data the picker reads without the
// balancer lock: expiresAt is fixed at creation and successor is the Java
// PickProgress CAS, so exactly one pick per SubConn requests a successor.
type expiringSubConn struct {
	sc        balancer.SubConn
	expiresAt time.Time
	successor atomic.Bool
}

func (b *expiringPickFirst) UpdateClientConnState(state balancer.ClientConnState) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if cfg, ok := state.BalancerConfig.(expiringPickFirstConfig); ok {
		b.maxAge = time.Duration(cfg.MaxAgeMillis) * time.Millisecond
	}

	// Endpoints supersede Addresses; the channel wraps Addresses into
	// Endpoints as well, so flatten whichever is present into one SubConn's
	// address list, like the Java agent's EquivalentAddressGroup list.
	var addrs []resolver.Address
	for _, endpoint := range state.ResolverState.Endpoints {
		addrs = append(addrs, endpoint.Addresses...)
	}
	if len(addrs) == 0 {
		addrs = state.ResolverState.Addresses
	}
	if len(addrs) == 0 {
		b.resolverErrorLocked(fmt.Errorf("%s: no addresses resolved", expiringPickFirstName))
		return balancer.ErrBadResolverState
	}

	// Existing SubConns keep their addresses: the passthrough resolver never
	// re-resolves, and a successor picks the new list up anyway.
	first := b.addrs == nil
	b.addrs = addrs
	if first {
		b.createSubConnLocked()
		b.updateBalancingStateLocked()
	}
	return nil
}

func (b *expiringPickFirst) ResolverError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resolverErrorLocked(err)
}

// resolverErrorLocked reports the error only while nothing is READY: unlike
// the Java agent's clear(), a working connection is never torn down over a
// name resolution problem.
func (b *expiringPickFirst) resolverErrorLocked(err error) {
	if b.closed || b.ready != nil {
		return
	}
	b.cc.UpdateState(balancer.State{
		ConnectivityState: connectivity.TransientFailure,
		Picker:            &expiringPicker{err: err},
	})
}

// UpdateSubConnState is unused: every SubConn registers a StateListener.
func (b *expiringPickFirst) UpdateSubConnState(balancer.SubConn, balancer.SubConnState) {}

func (b *expiringPickFirst) ExitIdle() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ready != nil || b.connecting != nil {
		return
	}
	b.createSubConnLocked()
	b.updateBalancingStateLocked()
}

func (b *expiringPickFirst) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, sd := range []*expiringSubConn{b.ready, b.connecting, b.failure} {
		if sd != nil {
			sd.sc.Shutdown()
		}
	}
	b.ready, b.connecting, b.failure = nil, nil, nil
}

// createSubConnLocked opens a new SubConn into the CONNECTING slot. With the
// slot taken it does nothing: the Java agent creates the SubConn and shuts it
// down on the conflict, which ends the same way.
func (b *expiringPickFirst) createSubConnLocked() {
	if b.closed || b.connecting != nil || len(b.addrs) == 0 {
		return
	}

	sd := &expiringSubConn{}
	sc, err := b.cc.NewSubConn(b.addrs, balancer.NewSubConnOptions{
		StateListener: func(state balancer.SubConnState) { b.onSubConnState(sd, state) },
	})
	if err != nil {
		Log("grpc").Warnf("%s: create subconn - %v", expiringPickFirstName, err)
		return
	}
	sd.sc = sc
	if b.maxAge > 0 {
		// Jittered so agents deployed together do not all rotate at once.
		sd.expiresAt = time.Now().Add(randomize(b.maxAge, streamAgeJitter))
	}
	b.connecting = sd
	sc.Connect()
	Log("grpc").Infof("%s: %v created", expiringPickFirstName, sc)
}

// onSubConnState is the StateListener: it moves sd to the slot of its new
// state, resolving a taken slot the way the Java agent's moveTo does.
func (b *expiringPickFirst) onSubConnState(sd *expiringSubConn, state balancer.SubConnState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}

	if b.ready == sd {
		b.ready = nil
	}
	if b.connecting == sd {
		b.connecting = nil
	}
	if b.failure == sd {
		b.failure = nil
	}

	switch state.ConnectivityState {
	case connectivity.Ready:
		if b.ready != nil {
			b.ready.sc.Shutdown()
			Log("grpc").Infof("%s: %v is replaced with %v", expiringPickFirstName, b.ready.sc, sd.sc)
		} else {
			Log("grpc").Infof("%s: %v is now READY", expiringPickFirstName, sd.sc)
		}
		b.ready = sd
	case connectivity.TransientFailure:
		if b.failure != nil {
			sd.sc.Shutdown()
			Log("grpc").Infof("%s: %v is shutdown by conflict in FAILURE", expiringPickFirstName, sd.sc)
		} else {
			b.failure = sd
			b.failureErr = state.ConnectionError
			Log("grpc").Infof("%s: %v is now on FAILURE - %v", expiringPickFirstName, sd.sc, state.ConnectionError)
		}
	case connectivity.Connecting, connectivity.Idle:
		// A grpc-go SubConn returns to IDLE after a failure back-off or when a
		// READY connection drops, and reconnects only when told to, so IDLE is
		// treated as a connection attempt and takes the CONNECTING slot.
		if b.connecting != nil {
			sd.sc.Shutdown()
			Log("grpc").Infof("%s: %v is shutdown by conflict in CONNECTING", expiringPickFirstName, sd.sc)
		} else {
			b.connecting = sd
			if state.ConnectivityState == connectivity.Idle {
				sd.sc.Connect()
			}
		}
	case connectivity.Shutdown:
	}

	b.updateBalancingStateLocked()
}

func (b *expiringPickFirst) updateBalancingStateLocked() {
	switch {
	case b.ready != nil:
		b.cc.UpdateState(balancer.State{
			ConnectivityState: connectivity.Ready,
			Picker:            &expiringPicker{b: b, sd: b.ready},
		})
	case b.connecting != nil:
		b.cc.UpdateState(balancer.State{
			ConnectivityState: connectivity.Connecting,
			Picker:            &expiringPicker{err: balancer.ErrNoSubConnAvailable},
		})
	case b.failure != nil:
		b.cc.UpdateState(balancer.State{
			ConnectivityState: connectivity.TransientFailure,
			Picker:            &expiringPicker{err: b.failureErr},
		})
	default:
		// An IDLE channel reconnects on its first pick, as pick_first does.
		b.cc.UpdateState(balancer.State{
			ConnectivityState: connectivity.Idle,
			Picker:            &expiringPicker{err: balancer.ErrNoSubConnAvailable, exitIdle: sync.OnceFunc(b.ExitIdle)},
		})
	}
}

// requestSuccessor runs on its own goroutine, the stand-in for the Java
// agent's SynchronizationContext: Pick must not block, and creating a SubConn
// takes the balancer lock and the channel's own locks.
func (b *expiringPickFirst) requestSuccessor(sd *expiringSubConn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Only the SubConn still serving needs a successor; one that was already
	// replaced or dropped meanwhile is reconnected by the IDLE path.
	if b.closed || b.ready != sd {
		return
	}
	Log("grpc").Infof("%s: %v reached its max age, creating a successor", expiringPickFirstName, sd.sc)
	b.createSubConnLocked()
}

// expiringPicker returns one fixed result. Pick reads nothing the balancer
// mutates: sd's fields are immutable or atomic.
type expiringPicker struct {
	b        *expiringPickFirst
	sd       *expiringSubConn
	err      error
	exitIdle func()
}

func (p *expiringPicker) Pick(balancer.PickInfo) (balancer.PickResult, error) {
	if p.exitIdle != nil {
		p.exitIdle()
	}
	if p.err != nil {
		return balancer.PickResult{}, p.err
	}
	// Exactly one pick per SubConn wins the CAS, so pick volume never
	// translates into SubConn volume.
	if !p.sd.expiresAt.IsZero() && time.Now().After(p.sd.expiresAt) && p.sd.successor.CompareAndSwap(false, true) {
		go p.b.requestSuccessor(p.sd)
	}
	return balancer.PickResult{SubConn: p.sd.sc}, nil
}
