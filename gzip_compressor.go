package pinpoint

import (
	"compress/gzip"
	"io"

	grpcgzip "google.golang.org/grpc/encoding/gzip"
)

// spanGzipWriters is how many gzip writers the span connection keeps between
// batches. A writer is about 1 MB of compressor state: kept, it is reused for
// the next batch; dropped, the next batch builds another. Two cover the batch
// sends that overlap at the e2e soak's rate; a burst past them builds writers
// that are dropped after the send, as every batch's was before.
const spanGzipWriters = 2

// gzipFreeList is the gzip compressor of the span connection, set with
// grpc.WithCompressor so it compresses that connection's calls alone.
//
// grpc-go's registered gzip compressor keeps its writers in a sync.Pool, which
// works for many calls a second on one goroutine and not for span batches:
// each batch is compressed on a goroutine of its own, a Put lands in the
// private slot of whichever P ran it, which no other P can take from, and
// every GC empties the pool. Over a 3 h soak at 100 RPS about one batch in
// four missed and built a new writer, and flate.NewWriter came to a quarter
// of everything the process allocated. A channel holds its writers across Ps
// and GCs.
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
