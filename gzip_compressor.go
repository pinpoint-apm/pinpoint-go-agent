package pinpoint

import (
	"compress/gzip"
	"io"

	"google.golang.org/grpc"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
)

// spanGzipWriters and statGzipWriters are how many gzip writers the span and
// stat connections keep between sends. A writer is about 1 MB of compressor
// state: kept, it is reused for the next send; dropped, the next send builds
// another. Two cover the span batch sends that overlap at the e2e soak's rate;
// a burst past them builds writers that are dropped after the send. The stat
// stream sends one message at a time.
const (
	spanGzipWriters = 2
	statGzipWriters = 1
)

// gzipFreeList is the gzip compressor of the span and stat connections, set
// with grpc.WithCompressor so it compresses that connection's calls alone.
//
// grpc-go's registered gzip compressor keeps its writers in a sync.Pool, which
// works for many calls a second on one goroutine and not for span batches:
// each batch is compressed on a goroutine of its own, a Put lands in the
// private slot of whichever P ran it, which no other P can take from, and
// every GC empties the pool. Over a 3 h soak at 100 RPS about one batch in
// four missed and built a new writer, and flate.NewWriter came to a quarter
// of everything the process allocated. The stat stream fared worse once it
// had grpc-go's pool to itself: at two messages a minute, a GC had emptied the
// pool before nearly every one, some 270 MB an hour. A channel holds its
// writers across Ps and GCs.
//
// The registered compressor is left as it is: its name is process-wide, so
// replacing it would change the compressor of the application's own gRPC
// clients and servers.
type gzipFreeList struct {
	free chan *gzip.Writer
}

func newGzipFreeList(writers int) *gzipFreeList {
	return &gzipFreeList{free: make(chan *gzip.Writer, writers)}
}

// Do compresses p into w with a kept writer, or a new one if none is free, and
// keeps the writer for the next call if there is room. A writer that failed is
// dropped rather than kept.
func (c *gzipFreeList) Do(w io.Writer, p []byte) error {
	var z *gzip.Writer
	select {
	case z = <-c.free:
		z.Reset(w)
	default:
		z = gzip.NewWriter(w)
	}
	if _, err := z.Write(p); err != nil {
		return err
	}
	if err := z.Close(); err != nil {
		return err
	}
	select {
	case c.free <- z:
	default:
	}
	return nil
}

// Type is the grpc-encoding the collector reads: the same "gzip" grpc-go's
// registered compressor sends, which also decompresses any gzip response.
func (c *gzipFreeList) Type() string {
	return grpcgzip.Name
}

// gzipDialOptions gives a collector connection a gzipFreeList of writers when
// Collector.Grpc.Compression is gzip, and reports whether it did.
func gzipDialOptions(config *Config, writers int) ([]grpc.DialOption, bool) {
	if config.String(CfgCollectorGrpcCompression) != compressionGzip {
		return nil, false
	}
	//lint:ignore SA1019 the per-connection compressor; UseCompressor only names the process-wide one
	return []grpc.DialOption{grpc.WithCompressor(newGzipFreeList(writers))}, true
}

// compressCallOptions compresses a call through its connection's own
// compressor, which only a call naming identity turns off, once a collector
// has refused gzip. A sender built without that connection, as the tests'
// are, sends what compress says.
func compressCallOptions(gzipConn, compress bool) []grpc.CallOption {
	switch {
	case gzipConn && !compress:
		return identityCallOptions
	case !gzipConn && compress:
		return gzipCallOptions
	}
	return nil
}
