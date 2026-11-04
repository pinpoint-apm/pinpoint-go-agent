package pinpoint

import (
	"bytes"
	"compress/gzip"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

var gzipTestPayload = []byte(strings.Repeat("span-event agent-id application-name /api/path 200 ", 400))

func gunzip(t *testing.T, compressed []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return out
}

// What the span connection sends must read back as the batch, through the
// gzip decompressor a gRPC peer uses.
func Test_gzipFreeList_compressesToGzip(t *testing.T) {
	c := newGzipFreeList(1)
	for range 2 { // a new writer, then the kept one
		var out bytes.Buffer
		require.NoError(t, c.Do(&out, gzipTestPayload))
		assert.Less(t, out.Len(), len(gzipTestPayload))
		assert.Equal(t, gzipTestPayload, gunzip(t, out.Bytes()))

		r, err := encoding.GetCompressor(c.Type()).Decompress(bytes.NewReader(out.Bytes()))
		require.NoError(t, err)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, gzipTestPayload, got)
	}
	assert.Equal(t, "gzip", c.Type())
}

// Each span batch is compressed on a goroutine of its own and GCs come between
// batches: the writer has to be the same one across both, where a sync.Pool
// gave it back to one P's private slot and lost it at the next GC.
func Test_gzipFreeList_keepsItsWriterAcrossGoroutinesAndGCs(t *testing.T) {
	c := newGzipFreeList(1)
	compressOnOwnGoroutine := func() {
		var wg sync.WaitGroup
		wg.Go(func() { assert.NoError(t, c.Do(io.Discard, gzipTestPayload)) })
		wg.Wait()
	}

	compressOnOwnGoroutine()
	require.Len(t, c.free, 1)
	first := <-c.free
	c.free <- first

	for range 5 {
		runtime.GC()
		compressOnOwnGoroutine()
	}
	require.Len(t, c.free, 1)
	assert.Same(t, first, <-c.free, "a batch built a new writer instead of reusing the kept one")
}

// A writer is about 1 MB, so the list keeps no more than its size however many
// batches overlap; the extra writers are dropped after their send.
func Test_gzipFreeList_keepsAtMostItsSize(t *testing.T) {
	c := newGzipFreeList(spanGzipWriters)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 10 {
		wg.Go(func() {
			<-start
			assert.NoError(t, c.Do(io.Discard, gzipTestPayload))
		})
	}
	close(start)
	wg.Wait()
	assert.LessOrEqual(t, len(c.free), spanGzipWriters)
	assert.Positive(t, len(c.free))
}

// The kept writer is what spares a batch the ~1 MB a new writer costs: across
// goroutines and GCs, the bytes allocated per batch stay far below that.
func Test_gzipFreeList_batchDoesNotAllocateAWriter(t *testing.T) {
	c := newGzipFreeList(1)
	require.NoError(t, c.Do(io.Discard, gzipTestPayload))

	const batches = 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range batches {
		var wg sync.WaitGroup
		wg.Go(func() { _ = c.Do(io.Discard, gzipTestPayload) })
		wg.Wait()
		if i%4 == 0 {
			runtime.GC()
		}
	}
	runtime.ReadMemStats(&after)
	perBatch := (after.TotalAlloc - before.TotalAlloc) / batches
	assert.Less(t, perBatch, uint64(128<<10), "a batch allocated %d bytes; a new gzip writer is about 1 MB", perBatch)
}

// The connection's own compressor is turned off only by naming identity, and
// a sender without that connection is left as it was; the span and stat
// senders both choose their call options this way.
func Test_spanGrpc_callOptions(t *testing.T) {
	for _, tt := range []struct {
		name     string
		gzipConn bool
		compress bool
		want     []grpc.CallOption
	}{
		{"gzip connection", true, true, nil},
		{"gzip connection after the collector refused gzip", true, false, identityCallOptions},
		{"Compression none", false, false, nil},
		{"no gzip connection, compress on", false, true, gzipCallOptions},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &spanGrpc{gzipConn: tt.gzipConn}
			s.compress.Store(tt.compress)
			assert.Equal(t, tt.want, s.callOptions())
		})
	}
}

// The stat connection, like the span one, is dialed with gzipFreeList when
// Compression is gzip: at a few messages a minute, grpc-go's pool was empty
// for nearly every one of them.
func Test_newStatGrpc_dialsItsOwnCompressor(t *testing.T) {
	for _, tt := range []struct {
		compression string
		want        bool
	}{{"gzip", true}, {"none", false}} {
		t.Run(tt.compression, func(t *testing.T) {
			cfg, err := NewConfig(WithAppName("TestApp"), WithCollectorHost("127.0.0.1"),
				WithCollectorGrpcCompression(tt.compression))
			require.NoError(t, err)
			s, err := newStatGrpc(newTestAgent(cfg))
			require.NoError(t, err)
			defer s.close()
			assert.Equal(t, tt.want, s.gzipConn)
			assert.Equal(t, tt.want, s.compress.Load())
		})
	}
}

// The span connection is dialed with gzipFreeList when Compression is gzip,
// and with no compressor of its own when it is none.
func Test_newSpanGrpc_dialsItsOwnCompressor(t *testing.T) {
	for _, tt := range []struct {
		compression string
		want        bool
	}{{"gzip", true}, {"none", false}} {
		t.Run(tt.compression, func(t *testing.T) {
			cfg, err := NewConfig(WithAppName("TestApp"), WithCollectorHost("127.0.0.1"),
				WithCollectorGrpcCompression(tt.compression))
			require.NoError(t, err)
			s, err := newSpanGrpc(newTestAgent(cfg))
			require.NoError(t, err)
			defer s.close()
			assert.Equal(t, tt.want, s.gzipConn)
			assert.Equal(t, tt.want, s.compress.Load())
		})
	}
}
