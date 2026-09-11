/*
 * Copyright 2020-present NAVER Corp.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Locked parity invariants.
//
// on. They are locked here so a later change has to state its intent instead
// of drifting one agent away from the other two: a failure means either the
// change is wrong, or all three implementations and doc/java_parity.md move
// together.
//
// for group, and doc/java_parity.md ("Locked parity invariants") is the table
// group exists there.
//
// Groups:
//   1  SQL normalization state machine
//   2  span event depth / sequence numbering
//   3  span chunk serialization
//   4  async id / sequence and span id sentinels
//   5  propagation header names and transaction id format
//   6  sampling formulas
//   7  URI histogram layout
//   8  active trace histogram layout
//   9  transaction counters
//  10  message truncation format
//  11  gRPC channel constants
//  12  error cause categories
//  13  queue overflow policy
//  14  proxy request header pipeline    (parsers: plugin/http/java_parity_lock_test.go)
//  15  logging level policy
//  16  shutdown contract                (port consensus, no Java counterpart)

package pinpoint

import (
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

// ===========================================================================
// Group 1 - SQL normalization state machine
// ===========================================================================

// (commons-profiler ParserContext.parse via DefaultSqlNormalizer's no-arg
// agent's SqlTest.JavaParityGoldenCases.
type javaParitySqlCase struct {
	name       string
	sql        string
	normalized string
	params     string
	// paramsUnsplittable marks a case whose param string cannot be split back
	// into one entry per placeholder: an unterminated literal writes its
	// content into param without emitting a placeholder, so the counts do not
	// below is skipped, never the byte-for-byte expectation.
	paramsUnsplittable bool
}

func javaParitySqlCases() []javaParitySqlCase {
	return []javaParitySqlCase{
		{
			name:       "number, escaped quote and double-quoted identifier",
			sql:        `select * from t where a = 1.5e3 and b = 'it''s' and c = "col1" -- comment`,
			normalized: `select * from t where a = 0# and b = '1$' and c = "col1" -- comment`,
			params:     `1.5e3,it''s`,
		},
		{
			name:       "unary minus, exponent and hex literal",
			sql:        `a = -1 and b = 1e-3 and c = 0x1F`,
			normalized: `a = -0# and b = 1#-2# and c = 3#x1F`,
			params:     `1,1e,3,0`,
		},
		{
			name:       "dotted identifier and comma inside a literal",
			sql:        `select t1.col2, t1.5 from t where id = 10 and name = 'a,b' and n2 = 'x''y'`,
			normalized: `select t1.col2, t1.5 from t where id = 0# and name = '1$' and n2 = '2$'`,
			params:     `10,a,,b,x''y`,
		},
		{
			name:               "backslash does not escape a quote",
			sql:                `s = 'a\'b' and n = 3`,
			normalized:         `s = '0$'b'`,
			params:             `a\, and n = 3`,
			paramsUnsplittable: true,
		},
		{
			name:       "hint, empty literal and multi-line comment",
			sql:        "select /*+ INDEX(t idx) */ 1, \"2\", '' , 'z' from t /* multi\n line 42 */ where x = ?",
			normalized: "select /*+ INDEX(t idx) */ 0#, \"1#\", '' , '2$' from t /* multi\n line 42 */ where x = ?",
			params:     `1,2,z`,
		},
		{
			name:       "multibyte identifiers enable a number token",
			sql:        `SELECT/*c*/1 FROM t WHERE 테이블1 = 2 and 名前 = '値'`,
			normalized: `SELECT/*c*/1 FROM t WHERE 테이블0# = 1# and 名前 = '2$'`,
			params:     `1,2,値`,
		},
		{
			name:       "dollar followed by a digit is an identifier",
			sql:        `select $1, $2 from t where a=$3 and b = 4`,
			normalized: `select $1, $2 from t where a=$3 and b = 0#`,
			params:     `4`,
		},
		{
			name:               "unterminated literal emits no placeholder",
			sql:                `select 'abc`,
			normalized:         `select '`,
			params:             `abc`,
			paramsUnsplittable: true,
		},
		{
			name:       "value tuples and a line comment",
			sql:        `insert into t values (1,2,3), ('a','b','c') // trailing`,
			normalized: `insert into t values (0#,1#,2#), ('3$','4$','5$') // trailing`,
			params:     `1,2,3,a,b,c`,
		},
		{
			name:       "empty literal consumes no index",
			sql:        `select '''' from t where a = 1`,
			normalized: `select '''' from t where a = 0#`,
			params:     `1`,
		},
		{
			name:       "block comment end token is searched past the opener",
			sql:        `/*/`,
			normalized: `/*/`,
			params:     ``,
		},
		{
			name:       "dollar not followed by a digit keeps the flag",
			sql:        `V$SESSION1`,
			normalized: `V$SESSION1`,
			params:     ``,
		},
		{
			name:       "shared index counter across numbers and literals",
			sql:        `$'x'1`,
			normalized: `$'0$'1#`,
			params:     `x,1`,
		},
		{
			name:       "exponent sign is a separate token",
			sql:        `1.4e-10`,
			normalized: `0#-1#`,
			params:     `1.4e,10`,
		},
		{
			name:       "digits after a dot are part of the identifier",
			sql:        `test.123`,
			normalized: `test.123`,
			params:     ``,
		},
		{
			name:       "underscore then space re-enables the number token",
			sql:        `test_ 123`,
			normalized: `test_ 0#`,
			params:     `123`,
		},
		{
			name:       "hash is not a comment",
			sql:        `select #1 from t`,
			normalized: `select #0# from t`,
			params:     `1`,
		},
		{
			name:       "bind markers are preserved",
			sql:        `select * from t where a in (?, ?, ?)`,
			normalized: `select * from t where a in (?, ?, ?)`,
			params:     ``,
		},
		{
			name:       "an IN list of literals is one statement per arity",
			sql:        `select * from t where a in (1,2,3)`,
			normalized: `select * from t where a in (0#,1#,2#)`,
			params:     `1,2,3`,
		},
	}
}

func Test_javaParityLock_SqlNormalizerGoldenCases(t *testing.T) {
	for _, tc := range javaParitySqlCases() {
		t.Run(tc.name, func(t *testing.T) {
			normalized, params := newSqlNormalizer(tc.sql, false).run()
			assert.Equal(t, tc.normalized, normalized, "normalized SQL is the id/UID cache key and PSqlMetaData.sql; it must match Java byte for byte")
			assert.Equal(t, tc.params, params, "param is split on ',' by the server to refill the placeholders")
		})
	}
}

// Test_javaParityLock_SqlNormalizerIsNotIdempotent locks that normalizing an
// already-normalized statement changes it again: `0#` becomes `0##`. All three
// agents behave this way, and a "fix" that made it idempotent would change
// every SQL id for statements the agent happens to see twice.
func Test_javaParityLock_SqlNormalizerIsNotIdempotent(t *testing.T) {
	once, _ := newSqlNormalizer(`select 1`, false).run()
	assert.Equal(t, `select 0#`, once)

	twice, _ := newSqlNormalizer(once, false).run()
	assert.Equal(t, `select 0##`, twice, "normalization is not idempotent in any of the three agents")
}

// Test_javaParityLock_SqlNormalizerWhitespaceIsNotNormalized locks that runs of
// whitespace survive verbatim - none of the three agents collapse them, so two
// statements differing only in spacing are two SQL ids.
func Test_javaParityLock_SqlNormalizerWhitespaceIsNotNormalized(t *testing.T) {
	normalized, _ := newSqlNormalizer("select   *\n\tfrom  t", false).run()
	assert.Equal(t, "select   *\n\tfrom  t", normalized)
}

// Test_javaParityLock_SqlNormalizerRemoveComments locks the agent default
// dropped rather than copied, and a statement that is nothing but a comment
// normalizes to the empty string.
func Test_javaParityLock_SqlNormalizerRemoveComments(t *testing.T) {
	normalized, params := newSqlNormalizer(`SELECT/*c*/1 FROM t`, true).run()
	assert.Equal(t, `SELECT1 FROM t`, normalized, "the comment is dropped and the digit stays an identifier digit: a comment does not re-enable the number token")
	assert.Equal(t, ``, params)

	normalized, params = newSqlNormalizer(`/* only */`, true).run()
	assert.Equal(t, ``, normalized)
	assert.Equal(t, ``, params)
}

// splitOutputParams is the agent-side counterpart of the server's
// OutputParameterParser: it splits param on ',' and un-escapes the doubled
func splitOutputParams(params string) []string {
	if params == "" {
		return nil
	}
	var (
		out []string
		cur strings.Builder
	)
	for i := 0; i < len(params); i++ {
		if params[i] != ',' {
			cur.WriteByte(params[i])
			continue
		}
		if i+1 < len(params) && params[i+1] == ',' {
			cur.WriteByte(',')
			i++
			continue
		}
		out = append(out, cur.String())
		cur.Reset()
	}
	out = append(out, cur.String())
	return out
}

// scanPlaceholderIndices returns the placeholder indices of a normalized
// statement in the order they appear. `<n>#` marks a number and `<n>$` a
// character literal; both draw from one shared counter, which is what makes
// the server able to refill them from a single comma-separated param string.
func scanPlaceholderIndices(normalized string) []int {
	var digits strings.Builder
	out := []int{}
	for i := 0; i < len(normalized); i++ {
		ch := normalized[i]
		if ch >= '0' && ch <= '9' {
			digits.WriteByte(ch)
			continue
		}
		if (ch == '#' || ch == '$') && digits.Len() > 0 {
			n := 0
			for _, d := range digits.String() {
				n = n*10 + int(d-'0')
			}
			out = append(out, n)
		}
		digits.Reset()
	}
	return out
}

// Test_javaParityLock_SqlNormalizerSharedIndexCounter locks the invariant the
// server depends on: placeholders are numbered 0..n-1 from one counter shared
// by numbers and literals, and there are exactly as many of them as there are
// params. This is the property that breaks first if either agent starts
// counting numbers and literals separately.
func Test_javaParityLock_SqlNormalizerSharedIndexCounter(t *testing.T) {
	for _, tc := range javaParitySqlCases() {
		if tc.paramsUnsplittable {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			normalized, params := newSqlNormalizer(tc.sql, false).run()
			indices := scanPlaceholderIndices(normalized)

			want := make([]int, len(indices))
			for i := range want {
				want[i] = i
			}
			assert.Equal(t, want, indices, "placeholder indices must run 0..n-1 in order")
			assert.Len(t, splitOutputParams(params), len(indices), "one param per placeholder")
		})
	}
}

// Test_javaParityLock_SqlNormalizerInputCapDropsTheWholeStatement locks the
// one deliberate divergence from Java in this group. A statement longer than
// maxSqlNormalizeLength (1 << 20, sql_util.go:47) is dropped whole: run()
// comes back with an empty normalized text and an empty param string, never
// with a cut. Java has no input cap at all - commons-profiler's
// DefaultSqlNormalizer walks a statement of any length - so the cap is a
// two-port addition, and the C++ agent carries the identical constant
// (kMaxNormalizedSqlLength) and the identical drop policy. What is locked
// here is the value and the policy being shared: a cut loses the placeholder
// whenever it lands inside a literal, and yields a SQL id / UID no other
// agent computes.
//
// The boundary cases - one byte either side of the cap, a multibyte
// character straddling it - belong to
// Test_sqlNormalizer_DropsInputPastTheNormalizationCap in sql_util_test.go
// and are not repeated here.
func Test_javaParityLock_SqlNormalizerInputCapDropsTheWholeStatement(t *testing.T) {
	assert.Equal(t, 1<<20, maxSqlNormalizeLength, "C++ kMaxNormalizedSqlLength")
	assert.Greater(t, maxSqlNormalizeLength, maxSqlSize, "the memory cap sits above the metadata cap")

	// A literal that runs past the cap: precisely the shape where a cut would
	// leave the opening quote without its placeholder.
	over := "select 1 from t where a = '" + strings.Repeat("x", maxSqlNormalizeLength) + "'"
	assert.Greater(t, len(over), maxSqlNormalizeLength)
	assert.False(t, sqlNormalizable(over))

	normalized, params := newSqlNormalizer(over, false).run()
	assert.Equal(t, "", normalized, "an over-cap statement is dropped whole, not cut")
	assert.Equal(t, "", params, "a cut param would leave placeholders the server cannot refill")

	// The cap measures the raw input and changes nothing else: a statement
	// within it normalizes exactly as any other.
	normalized, params = newSqlNormalizer("select 1", false).run()
	assert.Equal(t, "select 0#", normalized)
	assert.Equal(t, "1", params)
}

// ===========================================================================
// Group 2 - span event depth / sequence numbering
// ===========================================================================

// profiler.callstack.max.depth=64, profiler.callstack.max.sequence=5000,
// profiler.io.buffering.buffersize=20 (DefaultInstrumentConfig, pinpoint-root.config).
func Test_javaParityLock_SpanEventLimitDefaults(t *testing.T) {
	assert.Equal(t, 64, defaultEventDepth, "Java profiler.callstack.max.depth")
	assert.Equal(t, 5000, defaultEventSequence, "Java profiler.callstack.max.sequence")
	assert.Equal(t, 20, defaultEventChunkSize, "Java profiler.io.buffering.buffersize")
}

// Test_javaParityLock_SpanEventLimitFloors locks the floors this agent and the
// misconfigured limit cannot make the call stack unusable; the values must stay
// equal between the two ports.
func Test_javaParityLock_SpanEventLimitFloors(t *testing.T) {
	assert.Equal(t, 2, minEventDepth, "C++ defaults MIN_SPAN_MAX_EVENT_DEPTH")
	assert.Equal(t, 4, minEventSequence, "C++ defaults MIN_SPAN_MAX_EVENT_SEQUENCE")
}

// javaOverflowDecision is the overflow predicate all three agents implement.
// where index is the number of elements already on the stack. This agent stores
// the depth the next event would take (index+1), hence depth-1.
func javaOverflowDecision(sequence, depth, maxSequence, maxDepth int32) bool {
	return sequence >= maxSequence || depth-1 > maxDepth
}

// Test_javaParityLock_SpanEventOverflowDecision locks the boundaries the
// predicate draws: the effective deepest recorded level is maxDepth+1, and
// exactly maxSequence events are recorded.
func Test_javaParityLock_SpanEventOverflowDecision(t *testing.T) {
	const maxDepth, maxSequence = 3, 5

	// Depth: the push whose event would take depth maxDepth+1 is still
	// recorded; maxDepth+2 overflows.
	assert.False(t, javaOverflowDecision(0, int32(maxDepth), maxSequence, maxDepth))
	assert.False(t, javaOverflowDecision(0, int32(maxDepth)+1, maxSequence, maxDepth))
	assert.True(t, javaOverflowDecision(0, int32(maxDepth)+2, maxSequence, maxDepth))

	// Sequence: 0..maxSequence-1 are recorded, maxSequence overflows.
	assert.False(t, javaOverflowDecision(int32(maxSequence)-1, 1, maxSequence, maxDepth))
	assert.True(t, javaOverflowDecision(int32(maxSequence), 1, maxSequence, maxDepth))
}

// assertContiguousRange reports that got is a permutation of
// base..base+len(got)-1: every position handed out exactly once, with no gap.
func assertContiguousRange(t *testing.T, got []int32, base int32, what string) {
	t.Helper()

	seen := make(map[int32]bool, len(got))
	for _, v := range got {
		assert.False(t, seen[v], "%s %d handed out twice", what, v)
		seen[v] = true
	}
	for i := 0; i < len(got); i++ {
		assert.True(t, seen[base+int32(i)], "%s %d missing from the reserved range", what, base+int32(i))
	}
}

// Test_javaParityLock_SpanEventPositionIsReservedAtomically locks the pair
// reservation both ports added and Java does not have. Java relies on a
// single-thread call-stack contract instead - DefaultCallStack.push does
// sequence++ inside push - while this agent and the C++ agent hand the
// (sequence, depth) pair out of one atomic step (span.reserveEventPosition,
// span.go:671), because a span here may legitimately be driven from several
// goroutines of one call stack and a duplicate PSpanEvent.sequence is not a
// blurred call tree, it is one the collector cannot rebuild.
//
// Test_span_NewSpanEvent_ConcurrentSequencesAreUnique (span_test.go) already
// locks the sequence half through NewSpanEvent. The complementary property
// locked here is the primitive itself, and that the depth counter carries the
// same guarantee: under concurrency both coordinates come back unique and
// contiguous, so a pair is never handed out twice and never leaves a hole.
func Test_javaParityLock_SpanEventPositionIsReservedAtomically(t *testing.T) {
	const reservations = 256

	sp := newSampledSpan(newTestAgent(defaultConfig()), "op", "/rpc")
	sequences := make([]int32, reservations)
	depths := make([]int32, reservations)

	var wg sync.WaitGroup
	for i := 0; i < reservations; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sequences[i], depths[i] = sp.reserveEventPosition()
		}(i)
	}
	wg.Wait()

	// eventSequence starts at 0 and eventDepth at 1 (defaultSpan), so the
	// first event of a span is sequence 0 at depth 1 however the calls
	// interleave.
	assertContiguousRange(t, sequences, 0, "sequence")
	assertContiguousRange(t, depths, 1, "depth")

	// The counters are left where the reservations put them, so the next
	// event continues the range instead of reusing a position.
	assert.Equal(t, int32(reservations), sp.eventSequence.Load())
	assert.Equal(t, int32(reservations)+1, sp.eventDepth.Load())
}

// ===========================================================================
// Group 3 - span chunk serialization
// ===========================================================================

func parityEvent(sequence, depth int32, startTime int64) *spanEvent {
	return &spanEvent{sequence: sequence, depth: depth, startTime: startTime}
}

// Test_javaParityLock_ChunkKeyTimeAndStartElapsed locks the two values the
// the final chunk keys off the span's start time, a non-final chunk off its
// first event, and every startElapsed is the delta to the previous event
// (to keyTime for the first).
func Test_javaParityLock_ChunkKeyTimeAndStartElapsed(t *testing.T) {
	spanStart := time.UnixMilli(1_000)

	t.Run("final chunk keys off the span start time", func(t *testing.T) {
		chunk := &spanChunk{
			span:       &span{startTime: spanStart},
			eventChunk: []*spanEvent{parityEvent(0, 1, 1_050), parityEvent(1, 2, 1_120)},
			final:      true,
		}
		chunk.optimizeSpanEvents()

		assert.Equal(t, spanStart.UnixMilli(), chunk.keyTime)
		assert.Equal(t, int64(50), chunk.eventChunk[0].startElapsed, "first event is relative to keyTime")
		assert.Equal(t, int64(70), chunk.eventChunk[1].startElapsed, "later events are relative to the previous event")
	})

	t.Run("non-final chunk keys off its first event", func(t *testing.T) {
		chunk := &spanChunk{
			span:       &span{startTime: spanStart},
			eventChunk: []*spanEvent{parityEvent(0, 1, 1_050), parityEvent(1, 2, 1_120)},
			final:      false,
		}
		chunk.optimizeSpanEvents()

		assert.Equal(t, int64(1_050), chunk.keyTime)
		assert.Equal(t, int64(0), chunk.eventChunk[0].startElapsed)
		assert.Equal(t, int64(70), chunk.eventChunk[1].startElapsed)
	})
}

// Test_javaParityLock_ChunkSortsBySequence locks that a chunk is ordered by
// correct on a sorted chunk.
func Test_javaParityLock_ChunkSortsBySequence(t *testing.T) {
	chunk := &spanChunk{
		span:       &span{startTime: time.UnixMilli(1_000)},
		eventChunk: []*spanEvent{parityEvent(2, 1, 1_300), parityEvent(0, 1, 1_100), parityEvent(1, 1, 1_200)},
		final:      false,
	}
	chunk.optimizeSpanEvents()

	got := make([]int32, 0, len(chunk.eventChunk))
	for _, se := range chunk.eventChunk {
		got = append(got, se.sequence)
	}
	assert.Equal(t, []int32{0, 1, 2}, got)
	assert.Equal(t, int64(1_100), chunk.keyTime, "keyTime is the first event after sorting")
}

// Test_javaParityLock_ChunkSnapshotsEndPoint locks that a chunk copies the
// span's endPoint when it is cut. The sender serializes a non-final chunk while
// the span is still live on the request goroutine, so reading it back off the
// span there would race with SetEndPoint.
func Test_javaParityLock_ChunkSnapshotsEndPoint(t *testing.T) {
	sp := &span{startTime: time.UnixMilli(1_000)}
	sp.endPoint = "before"
	sp.cfg = &configSnapshot{spanEventChunkSize: defaultEventChunkSize}

	chunk := sp.newEventChunk(false)
	sp.endPoint = "after"

	assert.Equal(t, "before", chunk.endPoint, "the chunk must carry the endPoint it was cut with")
}

// Test_javaParityLock_ChunkDepthCompression is the depth half of
// GrpcSpanProcessorV2: an event at the same depth as its predecessor is sent
// with depth 0, which the collector reads as "same as previous".
func Test_javaParityLock_ChunkDepthCompression(t *testing.T) {
	chunk := &spanChunk{
		span:       &span{startTime: time.UnixMilli(1_000)},
		eventChunk: []*spanEvent{parityEvent(0, 2, 1_100), parityEvent(1, 2, 1_200), parityEvent(2, 3, 1_300)},
		final:      false,
	}
	chunk.optimizeSpanEvents()

	assert.Equal(t, int32(2), chunk.eventChunk[0].depth, "the first event always carries its real depth")
	assert.Equal(t, int32(0), chunk.eventChunk[1].depth, "same depth as the previous event compresses to 0")
	assert.Equal(t, int32(3), chunk.eventChunk[2].depth, "a change is sent explicitly")
}

// ===========================================================================
// Group 4 - async id / sequence and span id sentinels
// ===========================================================================

// is -1 and an async id of 0 means "no async context"; both agents skip these
// when they draw an id, so a drawn value can never be mistaken for "absent".
func Test_javaParityLock_Sentinels(t *testing.T) {
	assert.Equal(t, int64(-1), int64(noneSpanId), "Java SpanId.NULL")
	assert.Equal(t, int32(0), int32(noneAsyncId), "Java: asyncId 0 means no async context")
}

// Test_javaParityLock_GeneratedSpanIdIsNeverTheSentinel locks that a drawn span
// (SpanId.nextSpanID).
func Test_javaParityLock_GeneratedSpanIdIsNeverTheSentinel(t *testing.T) {
	for i := 0; i < 10_000; i++ {
		assert.NotEqual(t, int64(noneSpanId), generateSpanId())
	}
}

// ===========================================================================
// Group 5 - propagation header names and transaction id format
// ===========================================================================

// Test_javaParityLock_PropagationHeaderNames locks all ten header names against
// across a process boundary, with no error anywhere.
func Test_javaParityLock_PropagationHeaderNames(t *testing.T) {
	assert.Equal(t, "Pinpoint-TraceID", HeaderTraceId)
	assert.Equal(t, "Pinpoint-SpanID", HeaderSpanId)
	assert.Equal(t, "Pinpoint-pSpanID", HeaderParentSpanId)
	assert.Equal(t, "Pinpoint-Sampled", HeaderSampled)
	assert.Equal(t, "Pinpoint-Flags", HeaderFlags)
	assert.Equal(t, "Pinpoint-pAppName", HeaderParentApplicationName)
	assert.Equal(t, "Pinpoint-pAppType", HeaderParentApplicationType)
	assert.Equal(t, "Pinpoint-pAppNamespace", HeaderParentApplicationNamespace)
	assert.Equal(t, "Pinpoint-pServiceName", HeaderParentServiceName)
	assert.Equal(t, "Pinpoint-Host", HeaderHost)
}

// Test_javaParityLock_AnnotationKeys locks the annotation keys the agent emits.
// The collector and the web tier read them by number.
func Test_javaParityLock_AnnotationKeys(t *testing.T) {
	assert.Equal(t, 12, AnnotationApi)
	assert.Equal(t, 20, AnnotationSqlId)
	assert.Equal(t, 25, AnnotationSqlUid)
	assert.Equal(t, 40, AnnotationHttpUrl)
	assert.Equal(t, 46, AnnotationHttpStatusCode)
	assert.Equal(t, 300, AnnotationHttpProxyHeader)
	assert.Equal(t, -52, AnnotationExceptionChainId, "Java AnnotationKey.EXCEPTION_CHAIN_ID")
}

// Test_javaParityLock_TransactionIdFormat locks the wire format
// through the parser.
func Test_javaParityLock_TransactionIdFormat(t *testing.T) {
	tid := TransactionId{AgentId: "test-agent", StartTime: 1_600_000_000_000, Sequence: 42}
	assert.Equal(t, "test-agent^1600000000000^42", tid.String())

	agentId, startTime, sequence, ok := splitTransactionId(tid.String())
	assert.True(t, ok)
	assert.Equal(t, tid.AgentId, agentId)
	assert.Equal(t, tid.StartTime, startTime)
	assert.Equal(t, tid.Sequence, sequence)
}

// Test_javaParityLock_TransactionIdParsing locks the parser's accept/reject set.
// third delimiter, so "a^1^2^3" is the transaction "a^1^2" to all three agents.
func Test_javaParityLock_TransactionIdParsing(t *testing.T) {
	tests := []struct {
		tid string
		ok  bool
	}{
		{"agent.id_-09^1^2", true},
		{"a^1^2^3", true},
		{"bad agent^1^2", false},
		{"bad/agent^1^2", false},
		{"^1^2", false},
		{"agent^1", false},
		{"agent^x^2", false},
		{"agent^1^x", false},
		{"", false},
	}
	for _, tc := range tests {
		_, _, _, ok := splitTransactionId(tc.tid)
		assert.Equal(t, tc.ok, ok, "splitTransactionId(%q)", tc.tid)
	}

	agentId, startTime, sequence, ok := splitTransactionId("a^1^2^3")
	assert.True(t, ok)
	assert.Equal(t, "a", agentId)
	assert.Equal(t, int64(1), startTime)
	assert.Equal(t, int64(2), sequence, "the parser stops at the third delimiter")
}

// Test_javaParityLock_SampledHeaderEncoding locks that only the exact string
// else, "s1" or an absent header included, is sampled.
func Test_javaParityLock_SampledHeaderEncoding(t *testing.T) {
	const samplingFlagFalse = "s0"

	assert.Equal(t, "s0", samplingFlagFalse, "the off value is exactly \"s0\"")
	for _, v := range []string{"s1", "S0", "", "0", "false", "s00", " s0"} {
		assert.NotEqual(t, samplingFlagFalse, v, "%q must not disable sampling", v)
	}
}

// Test_javaParityLock_ParentAppTypeDefaultsToUndefined locks the parent
// application type recorded when Pinpoint-pAppName arrives without a
// parseable Pinpoint-pAppType: -1, ServiceType.UNDEFINED, which
// ServerRequestRecorder.recordParentInfo produces through
// NumberUtils.parseShort(type, ServiceType.UNDEFINED.getCode()). Both ports
// used to default to 1 (UNKNOWN), a real service type the server map drew as
// a node of that type.
func Test_javaParityLock_ParentAppTypeDefaultsToUndefined(t *testing.T) {
	assert.Equal(t, -1, defaultTestSpan().parentAppType, "a fresh span")

	for _, typ := range []string{"", "abc"} {
		span := defaultTestSpan()
		m := map[string]string{
			HeaderTraceId:               "t123456^12345^1",
			HeaderSpanId:                "67890",
			HeaderParentSpanId:          "123",
			HeaderParentApplicationName: "upstream",
		}
		if typ != "" {
			m[HeaderParentApplicationType] = typ
		}
		span.Extract(&DistributedTracingContextMap{m})
		assert.Equal(t, "upstream", span.parentAppName)
		assert.Equal(t, -1, span.parentAppType, "pAppType %q", typ)
	}
}

// ===========================================================================
// Group 6 - sampling formulas
// ===========================================================================

// Test_javaParityLock_CountingSamplerPhase locks the counting sampler's phase.
// the process is sampled and every rate-th one after it - not the rate-th
// request.
func Test_javaParityLock_CountingSamplerPhase(t *testing.T) {
	s := newRateSampler(3)

	var sampled []int
	for i := 1; i <= 10; i++ {
		if s.isSampled() {
			sampled = append(sampled, i)
		}
	}
	assert.Equal(t, []int{1, 4, 7, 10}, sampled, "the first call and every 3rd after it")
}

// TrueSampler and FalseSampler instead of CountingSampler.
func Test_javaParityLock_CountingSamplerEdgeRates(t *testing.T) {
	always := newRateSampler(1)
	for i := 0; i < 5; i++ {
		assert.True(t, always.isSampled(), "rate 1 samples everything")
	}

	never := newRateSampler(0)
	for i := 0; i < 5; i++ {
		assert.False(t, never.isSampled(), "rate 0 samples nothing")
	}

	clamped := newRateSampler(-7)
	for i := 0; i < 5; i++ {
		assert.False(t, clamped.isSampled(), "a negative rate is clamped to 0, not treated as unsigned")
	}
}

// PercentRateSampler adds the rate to a counter and samples on a remainder in
// (0, rate] - the first request lands on exactly rate and is sampled, where a
// [0, rate) window would sample the second one instead.
func Test_javaParityLock_PercentSamplerWindow(t *testing.T) {
	s := newPercentSampler(1) // rate 100 of 10000

	var sampled []int
	for i := 1; i <= 200; i++ {
		if s.isSampled() {
			sampled = append(sampled, i)
		}
	}
	assert.Equal(t, []int{1, 101}, sampled, "one per hundred, starting at the first call")
}

// does in PercentSamplerFactory: the percentage is multiplied by 100 and
// truncated, so anything under 0.01 collects nothing.
func Test_javaParityLock_PercentSamplerRateTruncation(t *testing.T) {
	assert.Equal(t, 10_000, samplingMaxPercentRate, "Java: 100 * 100")

	assert.Equal(t, uint64(10_000), newPercentSampler(100).rate)
	assert.Equal(t, uint64(10_000), newPercentSampler(150).rate, "over 100 is clamped to 100")
	assert.Equal(t, uint64(50), newPercentSampler(0.5).rate)
	assert.Equal(t, uint64(1), newPercentSampler(0.01).rate)
	assert.Equal(t, uint64(0), newPercentSampler(0.009).rate, "truncated to 0, i.e. never sampled")
	assert.Equal(t, uint64(0), newPercentSampler(-1).rate, "a negative percentage is clamped to 0")

	always := newPercentSampler(100)
	for i := 0; i < 5; i++ {
		assert.True(t, always.isSampled(), "100% is the TrueSampler case")
	}
	never := newPercentSampler(0)
	for i := 0; i < 5; i++ {
		assert.False(t, never.isSampled(), "0% is the FalseSampler case")
	}
}

// Test_javaParityLock_ThroughputLimiterInitialState locks the shape of the
// bucket behind every per-second throughput option (Sampling.NewThroughput,
// builds a Guava SmoothBursty whose initial storedPermits is 0: a fresh limiter
// injected through AllowN so the test is exact and sleep-free.
func Test_javaParityLock_ThroughputLimiterInitialState(t *testing.T) {
	const tps = 10 // one token per 100ms
	l := newTokenBucket(tps)
	now := time.Now()

	assert.True(t, l.AllowN(now, 1), "the first call passes")
	assert.False(t, l.AllowN(now, 1), "no token is due yet")
	assert.False(t, l.AllowN(now.Add(50*time.Millisecond), 1), "half an interval is not a token")
	assert.True(t, l.AllowN(now.Add(100*time.Millisecond), 1), "one interval elapsed, one token due")
	assert.False(t, l.AllowN(now.Add(100*time.Millisecond), 1))
}

// Test_javaParityLock_ThroughputLimiterCapacity locks the steady-state
// capacity at one second of permits, the maxBurstSeconds of RateLimiter.create:
// an idle bucket refills to exactly tps and no further, however long the idle.
func Test_javaParityLock_ThroughputLimiterCapacity(t *testing.T) {
	const tps = 10
	l := newTokenBucket(tps)
	idle := time.Now().Add(10 * time.Second)

	admitted := 0
	for i := 0; i < 2*tps; i++ {
		if l.AllowN(idle, 1) {
			admitted++
		}
	}
	assert.Equal(t, tps, admitted, "an idle bucket holds exactly one second of permits")
}

// The C++ agent now locks these same two throughput limiter properties in its
// mirror of this group (test/test_java_parity_lock.cpp, group 6), so the
// cross-reference reads both ways.

// ===========================================================================
// Group 7 - URI histogram layout
// ===========================================================================

// Test_javaParityLock_UrlStatHistogramBuckets locks the eight bucket bounds
// positionally, so a shifted boundary silently rewrites history.
func Test_javaParityLock_UrlStatHistogramBuckets(t *testing.T) {
	assert.Equal(t, 8, urlStatBucketSize)
	assert.Equal(t, 0, urlStatBucketVersion, "Java UriStatHistogramBucket.getVersion")

	tests := []struct {
		elapsed int64
		bucket  int
	}{
		{0, 0}, {99, 0},
		{100, 1}, {299, 1},
		{300, 2}, {499, 2},
		{500, 3}, {999, 3},
		{1_000, 4}, {2_999, 4},
		{3_000, 5}, {4_999, 5},
		{5_000, 6}, {7_999, 6},
		{8_000, 7}, {1_000_000, 7},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.bucket, getBucket(tc.elapsed), "getBucket(%d)", tc.elapsed)
	}
}

// Test_javaParityLock_UrlStatWindow locks the tick size and the number of
// closed ticks retained while the stat stream is down. Java's
// AsyncQueueingUriStatStorage.addCompletedData checks
// `snapshotQueue.size() > SNAPSHOT_LIMIT` (4) BEFORE offering, so the queue
// holds five; an earlier version of this test attributed a capacity of 4 to
// Java, which was the ports' own constant, not Java's.
func Test_javaParityLock_UrlStatWindow(t *testing.T) {
	assert.Equal(t, 30*time.Second, urlStatCollectInterval, "Java TickClock interval")
	assert.Equal(t, 5, maxCompletedUrlStatSnapshots, "Java snapshotQueue effective capacity (SNAPSHOT_LIMIT 4, checked before offer) / C++ kMaxCompletedSnapshots")
}

// Test_javaParityLock_UrlStatEmptyHistogram locks that an all-zero histogram
// decides on a count field; both ports decide on the bucket sum, so a single
// 0ms sample must still count as non-empty.
func Test_javaParityLock_UrlStatEmptyHistogram(t *testing.T) {
	hg := newStatHistogram()
	assert.True(t, hg.isEmpty(), "a fresh histogram is empty")

	hg.add(0)
	assert.False(t, hg.isEmpty(), "a 0ms sample lands in bucket 0 and is not empty")
	assert.Equal(t, int64(0), hg.total)
	assert.Equal(t, int32(1), hg.histogram[0])
}

// Test_javaParityLock_UrlStatUnknownKey locks the stand-in URL used when a span
// src/url_stat.h). Both the sampled and the unsampled span paths are exercised.
func Test_javaParityLock_UrlStatUnknownKey(t *testing.T) {
	const javaNullUri = "/NULL"
	assert.Equal(t, javaNullUri, urlStatUnknown)

	cfg := defaultConfig()
	cfg.Set(CfgHttpUrlStatEnable, true)
	agent := newTestAgent(cfg)
	agent.urlStatChan = make(chan *urlStat, 1)

	span := newSampledSpan(agent, "op", "/rpc")
	span.collectUrlStat(&UrlStatEntry{Method: "GET"}, false)
	assert.Equal(t, javaNullUri, span.urlStat.Url)

	unsampled := newUnSampledSpan(agent, "/rpc")
	unsampled.collectUrlStat(&UrlStatEntry{Method: "GET"}, false)
	assert.Equal(t, javaNullUri, unsampled.urlStat.Url)
}

// Test_javaParityLock_UrlStatTemplateIsFirstWriteWins locks the merge rule of
// mergeUrlStat (span.go:1057-1066): the URL template is first-write-wins with
// an explicit force override - MetricURLStat keeps the template already
// recorded, MetricURLStatForce replaces it - while the method and the status
// code are last-write-wins.
//
// The template half is Java's DefaultShared.setUriTemplate
// (DefaultShared.java:139-160): a null -> value compareAndSet, with the force
// overload setting unconditionally. A framework that recorded the matched
// route first must not have it replaced by a later, less precise layer. The
// status code half matches too - DefaultShared.setStatusCode
// (DefaultShared.java:128-131) is a plain setter, and a status is legitimately
// final only once the response exists.
//
// The method half is a port decision, not Java parity, and is locked here as
// such: DefaultShared.setHttpMethods (DefaultShared.java:168-177) is a
// compareAndSet like the template, so Java is first-wins on the method where
// both ports are last-wins. Recorded rather than asserted against Java so the
// next review does not read the Java citation as covering it.
func Test_javaParityLock_UrlStatTemplateIsFirstWriteWins(t *testing.T) {
	assert.Equal(t, "URLStat", MetricURLStat, "the metric name plugins record under")
	assert.Equal(t, "URLStatForce", MetricURLStatForce, "and the force variant")

	first := mergeUrlStat(nil, &UrlStatEntry{Url: "/route/{id}", Method: "GET"}, false)
	assert.Equal(t, "/route/{id}", first.Url)

	kept := mergeUrlStat(first, &UrlStatEntry{Url: "/route/7", Method: "POST", Status: 500}, false)
	assert.Equal(t, "/route/{id}", kept.Url, "the template is first-write-wins")
	assert.Equal(t, "POST", kept.Method, "the method is last-write-wins")
	assert.Equal(t, 500, kept.Status, "the status code is last-write-wins")

	forced := mergeUrlStat(kept, &UrlStatEntry{Url: "/route/override"}, true)
	assert.Equal(t, "/route/override", forced.Url, "MetricURLStatForce overrides the template")

	// The unknown stand-in is the absence of a template, not a value: it never
	// wins over a real one, in either direction.
	overUnknown := mergeUrlStat(&UrlStatEntry{Url: urlStatUnknown}, &UrlStatEntry{Url: "/late"}, false)
	assert.Equal(t, "/late", overUnknown.Url, "a recorded template replaces the unknown stand-in")

	underUnknown := mergeUrlStat(&UrlStatEntry{Url: "/early"}, &UrlStatEntry{Method: "GET"}, false)
	assert.Equal(t, "/early", underUnknown.Url, "an entry with no url does not erase the template")

	// The caller's entry is copied, so a later mutation of it cannot reach
	// the statistic the span kept.
	entry := &UrlStatEntry{Url: "/copied", Method: "GET"}
	merged := mergeUrlStat(nil, entry, false)
	entry.Method = "DELETE"
	assert.Equal(t, "GET", merged.Method, "the recorded entry is a copy")
}

// Test_javaParityLock_UrlStatWithoutAnEndTimeIsSkipped locks that an entry
// whose end time was never set is skipped entirely (url_stat.go:95-97), not
// keyed under tick 0: a zero tick would collect every such entry into one
// bucket at the epoch, and the web tier would draw it. Java does the same in
// AgentUriStatData.add (AgentUriStatData.java:56-64) - an endTime of 0 is
// logged and not added, and the URIKey is built from clock.tick(endTime) only
// after that check.
func Test_javaParityLock_UrlStatWithoutAnEndTimeIsSkipped(t *testing.T) {
	stats := newUrlStats(defaultConfig())

	stats.add(&urlStat{entry: &UrlStatEntry{Url: "/no-end"}, elapsed: 10})
	assert.True(t, stats.takeSnapshot(true).isEmpty(), "an entry with no end time is not collected")

	stats.add(&urlStat{entry: &UrlStatEntry{Url: "/ended"}, endTime: time.Now(), elapsed: 10})
	assert.False(t, stats.takeSnapshot(true).isEmpty(), "an entry with an end time is collected")
}

// ===========================================================================
// Group 8 - active trace histogram layout
// ===========================================================================

// Test_javaParityLock_ActiveTraceHistogram locks the four active-trace slots
// with an inclusive upper bound, so a span at exactly 1000ms is still "fast").
func Test_javaParityLock_ActiveTraceHistogram(t *testing.T) {
	now := time.UnixMilli(100_000)

	tests := []struct {
		elapsedMs int64
		slot      int
	}{
		{0, 0}, {1_000, 0},
		{1_001, 1}, {3_000, 1},
		{3_001, 2}, {5_000, 2},
		{5_001, 3}, {60_000, 3},
	}
	for _, tc := range tests {
		counts := []int32{0, 0, 0, 0}
		bucketActiveSpan(counts, now, now.Add(-time.Duration(tc.elapsedMs)*time.Millisecond))

		want := []int32{0, 0, 0, 0}
		want[tc.slot] = 1
		assert.Equal(t, want, counts, "an active span of %dms belongs in slot %d", tc.elapsedMs, tc.slot)
	}
}

// ===========================================================================
// Group 9 - transaction counters
// ===========================================================================

// DefaultTransactionCounter reports exist here and drain independently. A
// missing one shows up as a flat line in the Inspector, not as an error.
func Test_javaParityLock_TransactionCounters(t *testing.T) {
	stats := newAgentStats()

	stats.incrSampleNew()
	stats.incrSampleCont()
	stats.incrSampleCont()
	stats.incrUnSampleNew()
	stats.incrUnSampleNew()
	stats.incrUnSampleNew()
	stats.incrUnSampleCont()
	stats.incrSkipNew()
	stats.incrSkipCont()

	c := stats.drainCounters()
	assert.Equal(t, int64(1), c.sampleNew)
	assert.Equal(t, int64(2), c.sampleCont)
	assert.Equal(t, int64(3), c.unSampleNew)
	assert.Equal(t, int64(1), c.unSampleCont)
	assert.Equal(t, int64(1), c.skipNew)
	assert.Equal(t, int64(1), c.skipCont)

	drained := stats.drainCounters()
	assert.Equal(t, int64(0), drained.sampleNew, "a drain resets the counters")
	assert.Equal(t, int64(0), drained.sampleCont)
	assert.Equal(t, int64(0), drained.unSampleNew)
	assert.Equal(t, int64(0), drained.unSampleCont)
	assert.Equal(t, int64(0), drained.skipNew)
	assert.Equal(t, int64(0), drained.skipCont)
}

// ===========================================================================
// Group 10 - message truncation format
// ===========================================================================

// StringUtils.abbreviate writes: the value cut to the limit followed by
// "...(original length)". The web tier shows the marker as-is, so the format is
// part of the contract.
func Test_javaParityLock_TruncationFormat(t *testing.T) {
	assert.Equal(t, "short", abbreviateString("short", 10), "a value within the limit is untouched")
	assert.Equal(t, "0123456789", abbreviateString("0123456789", 10), "exactly at the limit is untouched")
	assert.Equal(t, "0123456789...(11)", abbreviateString("0123456789A", 10), "the marker carries the original length")
}

// Test_javaParityLock_TruncationCutsOnARuneBoundary locks the UTF-8 guard both
// a mid-rune cut would fail the whole span or metadata send carrying it.
func Test_javaParityLock_TruncationCutsOnARuneBoundary(t *testing.T) {
	// "가" is three bytes; a limit of 4 lands inside the second rune.
	got := abbreviateString("가가가", 4)
	assert.True(t, strings.HasPrefix(got, "가"))
	assert.Equal(t, "가...(9)", got)
}

// AbstractRecorder abbreviates an exception message to 256 chars before
// recording it on a span or span event, and
// profiler.exceptiontrace.errormessage.max defaults to 2048 for one exception
// metadata entry.
func Test_javaParityLock_MessageLimits(t *testing.T) {
	assert.Equal(t, 256, maxErrorMessageSize, "Java AbstractRecorder.recordException")
	assert.Equal(t, 2048, maxExceptionMessageSize, "Java profiler.exceptiontrace.errormessage.max")
	assert.Equal(t, 64*1024, maxSqlSize, "Java profiler.jdbc.maxsqllength")
}

// ===========================================================================
// Group 11 - gRPC channel constants
// ===========================================================================

// Test_javaParityLock_CollectorPortDefaults locks the three collector ports.
func Test_javaParityLock_CollectorPortDefaults(t *testing.T) {
	assert.Equal(t, 9991, cfgBaseMap[CfgCollectorAgentPort].defaultValue, "Java profiler.transport.grpc.agent.collector.port")
	assert.Equal(t, 9992, cfgBaseMap[CfgCollectorStatPort].defaultValue, "Java profiler.transport.grpc.stat.collector.port")
	assert.Equal(t, 9993, cfgBaseMap[CfgCollectorSpanPort].defaultValue, "Java profiler.transport.grpc.span.collector.port")
}

// Test_javaParityLock_GrpcChannelDefaults locks the channel options that were
// verified equal across the three agents. flowControlWindow, writeBufferSize
// three at the C-core defaults, which doc/java_parity.md records. The idle
// timeout is deliberately not locked: all three agents disable idling, but
// decision is shared, not the value (doc/java_parity.md, "gRPC channel
// arguments").
func Test_javaParityLock_GrpcChannelDefaults(t *testing.T) {
	assert.Equal(t, 30_000, grpcKeepAliveTime, "Java ClientOption keepAliveTime")
	assert.Equal(t, 60_000, grpcKeepAliveTimeout, "Java ClientOption keepAliveTimeout")
	assert.False(t, grpcKeepAlivePermitWithoutCalls, "Java ClientOption keepAliveWithoutCalls")
	assert.Equal(t, 4*1024*1024, grpcMaxMessageSize, "Java ClientOption maxInboundMessageSize")
	assert.Equal(t, 1*1024*1024, grpcFlowControlWindow, "Java ClientOption flowControlWindow")
	assert.Equal(t, 8*1024, grpcMaxHeaderListSize, "Java ClientOption maxHeaderListSize")
	assert.Equal(t, 0, grpcConnectionMaxAge, "renewal off, as in Java")
	assert.Equal(t, 0, grpcStreamMaxAge, "renewal off, as in Java")
}

// Test_javaParityLock_ReconnectBackoff locks the reconnect backoff shape: a
// x1.2 ramp from 3s to a 30s ceiling, randomized +/-30%, with the jitter
// applied after the clamp so a capped interval lands within +/-30% of the
// ceiling rather than always on it.
func Test_javaParityLock_ReconnectBackoff(t *testing.T) {
	assert.Equal(t, 3*time.Second, backOffInitialInterval)
	assert.Equal(t, 1.2, backOffMultiplier)
	assert.Equal(t, 30*time.Second, backOffMaxInterval)
	assert.Equal(t, 0.3, backOffJitter)

	within := func(attempt int, base time.Duration) {
		lo := time.Duration(float64(base) * (1 - backOffJitter))
		hi := time.Duration(float64(base) * (1 + backOffJitter))
		for i := 0; i < 200; i++ {
			d := backOffSleep(attempt)
			assert.GreaterOrEqual(t, d, lo, "attempt %d below the jitter window", attempt)
			assert.LessOrEqual(t, d, hi, "attempt %d above the jitter window", attempt)
		}
	}
	within(0, 3*time.Second)
	within(1, 3600*time.Millisecond)
	within(100, backOffMaxInterval)
}

// Test_javaParityLock_AgentInfoSchedule locks the AgentInfo refresh cadence.
// (profiler.agentInfo.send.retry.interval): registration gates tracing in both
// ports, so it has to retry far more often. doc/java_parity.md records that.
func Test_javaParityLock_AgentInfoSchedule(t *testing.T) {
	assert.Equal(t, 24*60*60*1000, defaultAgentInfoRefreshInterval, "Java AgentInfoSender refresh interval")
	assert.Equal(t, 3, defaultAgentInfoMaxTryPerAttempt, "Java AgentInfoSender maxTryPerAttempt")
	assert.Equal(t, 3000, defaultAgentInfoSendRetryInterval, "matches the C++ agent, not Java's effective 300000ms - see doc/java_parity.md")
}

// Test_javaParityLock_SqlCacheLengthLimitAppliesToTheUidCacheOnly locks the
// asymmetry two consecutive cross-agent reviews have raised as a false
// positive: the SQL cache length limit bounds the UID cache and never the id
// cache, in all three agents.
//
// Java: UidCache.put (UidCache.java:17-23) bypasses the cache for a key at or
// past its bypassLength and hands back a freshly computed UID, while the id
// cache SimpleCacheFactory.newSqlCache builds (SimpleCacheFactory.java:42-44)
// is a plain SimpleCache with no length check at all. The reason is not
// tidiness: an id comes from a sequence, so a bypassed statement would burn a
// new id - and a new sqlMeta - on every single execution, and the same query
// would show up in the web tier as a fresh entry per use. A UID is a hash of
// the text, so bypassing costs only the re-send.
//
// Asserted as behaviour rather than as a constant: sqlCacheable
// (agent.go:1435) is applied in cacheSqlUid (agent.go:1498) and normalizeSql
// (agent.go:1559), and deliberately not in cacheSql.
func Test_javaParityLock_SqlCacheLengthLimitAppliesToTheUidCacheOnly(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	agent.sqlCacheLengthLimit = 32

	const short = "select 1"
	long := "select " + strings.Repeat("x", 64)
	assert.True(t, agent.sqlCacheable(short), "within the limit")
	assert.False(t, agent.sqlCacheable(long), "past the limit")

	// The id cache takes both: an id is drawn from a sequence, so a bypass
	// would mint a new one per execution.
	assert.NotZero(t, agent.cacheSql(short))
	assert.NotZero(t, agent.cacheSql(long))
	_, shortHasId := agent.sqlCache.peek(short)
	_, longHasId := agent.sqlCache.peek(long)
	assert.True(t, shortHasId, "a short statement is cached by id")
	assert.True(t, longHasId, "the length limit does not apply to the id cache")

	// The UID cache takes only what fits: an over-limit statement still gets
	// a UID, computed from the text, but is not kept.
	assert.NotEmpty(t, agent.cacheSqlUid(short))
	assert.NotEmpty(t, agent.cacheSqlUid(long))
	_, shortHasUid := agent.sqlUidCache.peek(short)
	_, longHasUid := agent.sqlUidCache.peek(long)
	assert.True(t, shortHasUid, "a short statement is cached by uid")
	assert.False(t, longHasUid, "the length limit bypasses the uid cache")

	// Bypassing the cache must not change the value it would have returned:
	// the same id and the same UID come back for a repeated statement.
	assert.Equal(t, agent.cacheSql(long), agent.cacheSql(long), "the id is stable")
	assert.Equal(t, agent.cacheSqlUid(long), agent.cacheSqlUid(long), "the uid is stable")
}

// ===========================================================================
// Group 12 - error cause categories
// ===========================================================================

// Test_javaParityLock_ErrorCategoryBits locks the four cause bits carried in
// PSpan.err (tracer.go:112-131). They are a wire contract, not an internal
// detail: the collector and the web tier tell an exception apart from a
// failing HTTP status by the bit, so renumbering one silently rewrites what
// the UI says every affected transaction failed of. Java declares the same
// four in ErrorCategory (ErrorCategory.java:19-23).
func Test_javaParityLock_ErrorCategoryBits(t *testing.T) {
	assert.Equal(t, ErrorCategory(1<<0), ErrorCategoryUnknown, "Java ErrorCategory.UNKNOWN")
	assert.Equal(t, ErrorCategory(1<<1), ErrorCategoryException, "Java ErrorCategory.EXCEPTION")
	assert.Equal(t, ErrorCategory(1<<2), ErrorCategoryHttpStatus, "Java ErrorCategory.HTTP_STATUS")
	assert.Equal(t, ErrorCategory(1<<3), ErrorCategorySql, "Java ErrorCategory.SQL")
	assert.Equal(t, ErrorCategory(15), allErrorCategories, "Java EnumSet.allOf(ErrorCategory.class)")
}

// Test_javaParityLock_ErrorMarkMaskResolution locks how Span.ErrorMark and
// Span.ErrorMarkExclude resolve into the mask of categories allowed to fail a
// transaction (parseErrorMarkMask, config.go:463-473), against Java
// ConfigurableErrorRecorderFactory.getEnabledTypes
// (ConfigurableErrorRecorderFactory.java:28-61): an unset mark enables every
// category, exclude is subtracted from it, and UNKNOWN is added back last
// whatever the two lists say. Tokens are trimmed and lower-cased before
// matching exception / http-status / sql, and an unrecognised token is warned
// about and ignored rather than failing the parse - Java's
// `default: logger.warn(...)` arm.
func Test_javaParityLock_ErrorMarkMaskResolution(t *testing.T) {
	tests := []struct {
		name    string
		mark    []string
		exclude []string
		want    ErrorCategory
	}{
		{name: "an unset mark enables every category",
			want: allErrorCategories},
		{name: "a mark is the whole allow list, plus unknown",
			mark: []string{"exception"}, want: ErrorCategoryUnknown | ErrorCategoryException},
		{name: "exclude subtracts from the default everything",
			exclude: []string{"http-status"}, want: allErrorCategories &^ ErrorCategoryHttpStatus},
		{name: "exclude wins over mark",
			mark: []string{"exception", "sql"}, exclude: []string{"sql"},
			want: ErrorCategoryUnknown | ErrorCategoryException},
		{name: "unknown is re-added after the subtraction",
			mark: []string{"exception"}, exclude: []string{"exception"}, want: ErrorCategoryUnknown},
		{name: "unknown is not selectable and cannot be excluded",
			exclude: []string{"unknown"}, want: allErrorCategories},
		{name: "unknown has no spelling of its own in a mark",
			mark: []string{"unknown"}, want: ErrorCategoryUnknown},
		{name: "excluding every named category still leaves unknown",
			exclude: []string{"exception", "http-status", "sql"}, want: ErrorCategoryUnknown},
		{name: "tokens match case-insensitively",
			mark: []string{"EXCEPTION", "Http-Status", "sQl"}, want: allErrorCategories},
		{name: "surrounding space is trimmed",
			mark: []string{"  exception  "}, want: ErrorCategoryUnknown | ErrorCategoryException},
		{name: "one entry may carry a comma separated list",
			mark: []string{"exception,sql"},
			want: ErrorCategoryUnknown | ErrorCategoryException | ErrorCategorySql},
		{name: "an empty token is skipped, leaving an empty rather than a default set",
			mark: []string{""}, want: ErrorCategoryUnknown},
		{name: "an unrecognised token is ignored and the rest still resolves",
			mark: []string{"exception", "nonsense"},
			want: ErrorCategoryUnknown | ErrorCategoryException},
		{name: "an unrecognised token in exclude subtracts nothing",
			exclude: []string{"nonsense"}, want: allErrorCategories},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parseErrorMarkMask(tc.mark, tc.exclude))
		})
	}
}

// Test_javaParityLock_ExcludedCategoryRecordsNothing locks the recorder half:
// a category the operator removed records nothing at all - not the category
// bit, and not an ErrorCategoryUnknown fallback either. markSpanError
// (span.go:211-217) is the single point that writes span.err and it applies
// the mask there, exactly as Java's ConfigurableErrorRecorder.recordError
// (ConfigurableErrorRecorder.java:20-24) masks the error code only when the
// category is in the enabled set, with no else branch.
func Test_javaParityLock_ExcludedCategoryRecordsNothing(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSpanErrorMarkExclude, []string{"exception"})
	agent := newTestAgent(cfg)

	excluded := newSampledSpan(agent, "op", "/rpc")
	excluded.SetFailure(ErrorCategoryException)
	assert.Equal(t, int32(0), excluded.err.Load(),
		"an excluded category records nothing, not even ErrorCategoryUnknown")
	assert.Equal(t, int32(0), excluded.statusErr.Load(),
		"and the whole verdict is dropped, so the url stat is not failed either")

	// The categories that survived still record, and a SetFailure that names
	// none reports ErrorCategoryUnknown - always enabled.
	kept := newSampledSpan(agent, "op", "/rpc")
	kept.SetFailure(ErrorCategoryHttpStatus)
	kept.SetFailure()
	assert.Equal(t, int32(ErrorCategoryHttpStatus|ErrorCategoryUnknown), kept.err.Load(),
		"a transaction that failed for several reasons reports all of them")
}

// ===========================================================================
// Group 13 - queue overflow policy
// ===========================================================================

// Test_javaParityLock_SpanQueueHeadDrops locks the overflow policy of the span
// queue: a full queue discards its OLDEST chunk and counts the drop, so what
// survives a collector outage is the most recent traces.
//
// That matches Java's DEFAULT span sender, verified in the Java tree rather
// than assumed:
//
//   - agent-module/agent/src/main/resources/pinpoint-root.config:135 ships
//     profiler.transport.grpc.span.sender.type=BATCH (the release profile
//     repeats it, profiles/release/pinpoint.config:25), so
//     SpanBatchGrpcDataSender is what a stock Java agent runs;
//   - SpanBatchGrpcDataSender.send (SpanBatchGrpcDataSender.java:95-111)
//     offers, and on a full queue polls the head away - logging "discard
//     oldest message" - before re-offering the new one.
//
// An earlier cross-agent review cited GrpcDataSender.send
// (GrpcDataSender.java:56-68) instead and called this a divergence. That is
// the STREAM sender's base class: it tail-drops ("reject message") and is
// reached only when sender.type is set to STREAM. The citation has been wrong
// five times; it is recorded here so a sixth review checks it against the
// config default above before raising it again.
//
// span_queue_test.go covers the queue itself - shard capacity, the saturation
// hint, close and drain. What is locked here is the policy: which end is
// dropped, and that the drop is observable.
func Test_javaParityLock_SpanQueueHeadDrops(t *testing.T) {
	// A capacity below spanQueueMinShardCap gives a single shard, so the ring
	// is strictly FIFO and the surviving set is exact rather than the
	// unspecified cross-shard order.
	const capacity, enqueued = 4, 7
	q := newSpanQueue(capacity)
	assert.Len(t, q.shards, 1, "the test relies on a single, strictly FIFO shard")

	for i := 0; i < enqueued; i++ {
		assert.True(t, q.enqueue(&spanChunk{keyTime: int64(i)}),
			"enqueue never rejects before close: the drop is the oldest, not the arrival")
	}

	assert.Equal(t, capacity, q.length(), "the queue stays at its bound")
	assert.Equal(t, int64(enqueued-capacity), q.dropCount(),
		"every head drop is counted, as Java's discard-oldest log line is")

	var survived []int64
	for {
		chunk, ok := q.tryDequeue()
		if !ok {
			break
		}
		survived = append(survived, chunk.keyTime)
	}
	assert.Equal(t, []int64{3, 4, 5, 6}, survived, "the newest survive, the oldest are discarded")
}

// ===========================================================================
// Group 14 - proxy request header pipeline
// ===========================================================================

// The four proxy request parsers (apache, nginx, app, user) live in package
// plugin/http, which imports this package: a test in package pinpoint cannot
// reach them without an import cycle, and nothing there is worth moving to
// make it reachable. The parser half of this group therefore lives in
// plugin/http/java_parity_lock_test.go, under the same group number and
// title, and locks:
//
//   - all four parsers run independently, so a request that crossed two
//     proxies records two annotations rather than only the hop nearest the
//     agent (Java DefaultProxyRequestRecorder.java:52-53);
//   - each is gated on a positive received time, so t=0, a missing t= or one
//     that does not parse records nothing at all (Java
//     DefaultProxyRequestRecorder.java:71, via ProxyRequestHeader.isValid);
//   - nginx t= and D= accept only sec.mmm - exactly three decimals - and are
//     computed with integer arithmetic, never a float multiply (Java
//     NginxRequestParser.java:74-110).
//
// What is reachable from here is the parent half of the same pipeline.

// Test_javaParityLock_ParentInfoRequiresAParentAppName locks that a PSpan
// carries a PParentInfo only when parentAppName is non-empty
// (grpc.go:1690-1697) - the same guard Java puts on the whole parent block:
// ServerRequestRecorder.record calls recordParentInfo only for a non-root span
// (ServerRequestRecorder.java:76), and recordParentInfo records nothing when
// the Pinpoint-pAppName header is absent (ServerRequestRecorder.java:82-83).
//
// This is the invariant that stops the acceptor-host fallback both ports
// recently added from inventing a parent node. The fallback fills in the
// acceptor host when the peer sent no Pinpoint-Host, and it sits inside the
// parent-app-name check on both sides - Java's getAcceptorHost call is within
// the same if - so a span carrying an acceptor host but no parent application
// never ships a PParentInfo naming an empty application, which the server map
// would draw as an unnamed caller node.
//
// Locked at the message builder rather than at a sender, because that is the
// single point both the span and the span batch path go through;
// Test_spanGrpc_sendSpanBatch_carriesParentInfo and
// Test_spanGrpc_sendSpanBatch_rootSpanHasNoParentInfo (grpc_test.go) cover
// the sender path.
func Test_javaParityLock_ParentInfoRequiresAParentAppName(t *testing.T) {
	agent := newTestAgent(defaultConfig())

	// Set on both paths: an acceptor host alone must not produce a parent.
	orphan := newSampledSpan(agent, "op", "/rpc")
	orphan.acceptorHost = "api.example.com:8080"
	orphan.parentServiceName = "parent-service"
	orphan.NewSpanEvent("op")
	orphanSpan := (&spanMessageBuilder{}).makePSpan(orphan.newEventChunk(true)).GetSpan()
	assert.NotNil(t, orphanSpan.GetAcceptEvent(), "the accept event is always there")
	assert.Nil(t, orphanSpan.GetAcceptEvent().GetParentInfo(),
		"an acceptor host on its own must not invent a parent node")

	child := newSampledSpan(agent, "op", "/rpc")
	child.acceptorHost = "api.example.com:8080"
	child.parentAppName = "ParentApp"
	child.parentAppType = 1010
	child.parentServiceName = "parent-service"
	parent := (&spanMessageBuilder{}).makePSpan(child.newEventChunk(true)).GetSpan().GetAcceptEvent().GetParentInfo()
	if assert.NotNil(t, parent, "a named parent is described") {
		assert.Equal(t, "ParentApp", parent.GetParentApplicationName())
		assert.Equal(t, int32(1010), parent.GetParentApplicationType())
		assert.Equal(t, "parent-service", parent.GetParentServiceName())
		assert.Equal(t, "api.example.com:8080", parent.GetAcceptorHost())
	}
}

// ===========================================================================
// Group 15 - logging level policy
// ===========================================================================

// Test_javaParityLock_UnsupportedLogLevelKeepsTheCurrentLevel locks a
// TWO-PORT CONSENSUS, not Java parity: Java's level comes from a log4j2
// configuration file, which fails or falls back on its own terms and has no
// notion of "keep what is running". Both ports decided that an unsupported
// level string leaves the current level exactly where it was and says so in
// the log (logger.go:84-88), because silently ignoring a typo looks like a
// successful change, and on a config reload it would leave an operator
// debugging at the old level with no line explaining why.
//
// warn and warning are both accepted: the agent writes "warning" on its own
// lines, and refusing the spelling every other logger uses would hit exactly
// that reload path.
func Test_javaParityLock_UnsupportedLogLevelKeepsTheCurrentLevel(t *testing.T) {
	l := newLogger()

	l.setLevel("error")
	assert.Equal(t, logrus.ErrorLevel, l.defaultLogger.GetLevel())

	for _, unsupported := range []string{"warnign", "", "WARN ", "2", "verbose"} {
		l.setLevel(unsupported)
		assert.Equal(t, logrus.ErrorLevel, l.defaultLogger.GetLevel(),
			"%q must leave the level where it was", unsupported)
	}

	// Both spellings of warn are accepted, and each is a real level change.
	for _, name := range []string{"warn", "warning"} {
		l.setLevel("error")
		l.setLevel(name)
		assert.Equal(t, logrus.WarnLevel, l.defaultLogger.GetLevel(), "%q is accepted", name)
	}
}

// Test_javaParityLock_ConfigRejectsAnUnsupportedLogLevel locks the same rule
// one layer up, where an operator actually meets it (config.go:1144-1153): a
// value that is not one of trace, debug, info, warn or error keeps the level
// already published, or the default on the first publish. logrus itself would
// also take fatal and panic, which would silence warn and error while looking
// like a valid setting, so the agent refuses them too.
func Test_javaParityLock_ConfigRejectsAnUnsupportedLogLevel(t *testing.T) {
	t.Cleanup(func() { logger.setLevel("info") })

	tests := []struct {
		set  string
		want string
	}{
		{"trace", "trace"},
		{"debug", "debug"},
		{"info", "info"},
		{"warn", "warn"},
		{"warning", "warning"},
		{"error", "error"},
		{"verbose", "info"},
		{"fatal", "info"},
		{"panic", "info"},
	}
	for _, tc := range tests {
		t.Run(tc.set, func(t *testing.T) {
			c, err := NewConfig(WithAppName("javaParityLogLevel"), WithLogLevel(tc.set))
			assert.NoError(t, err)
			assert.Equal(t, tc.want, c.String(CfgLogLevel),
				"Log.Level = %q resolves to %q", tc.set, tc.want)
		})
	}
}

// Test_javaParityLock_LogRotationDefaults locks the rotation defaults and the
// floor under Log.MaxBackups. Also a two-port consensus: Java rotates through
// log4j2 policies, not through agent config keys. 0 backups reads as "keep
// none" to one reader and "keep every backup" to another - it is what
// lumberjack means by it - so a value below 1 is restored to the default of 1
// rather than honoured.
func Test_javaParityLock_LogRotationDefaults(t *testing.T) {
	t.Cleanup(func() { logger.setLevel("info") })

	assert.Equal(t, 1, defaultLogMaxBackups, "one rotated file is kept")
	assert.Equal(t, defaultLogMaxBackups, cfgBaseMap[CfgLogMaxBackups].defaultValue)
	assert.Equal(t, 10, cfgBaseMap[CfgLogMaxSize].defaultValue, "10 MB before rotation")

	for _, backups := range []int{0, -1} {
		c, err := NewConfig(WithAppName("javaParityLogRotation"), WithLogMaxBackups(backups))
		assert.NoError(t, err)
		assert.Equal(t, defaultLogMaxBackups, c.Int(CfgLogMaxBackups),
			"Log.MaxBackups = %d is restored to the default, not honoured", backups)
		assert.Equal(t, 10, c.Int(CfgLogMaxSize), "and an unset Log.MaxSize stays at 10 MB")
	}

	c, err := NewConfig(WithAppName("javaParityLogRotation"), WithLogMaxSize(0))
	assert.NoError(t, err)
	assert.Equal(t, 10, c.Int(CfgLogMaxSize), "Log.MaxSize below 1 is restored to the default")
}

// ===========================================================================
// Group 16 - shutdown contract (port consensus, no Java counterpart)
// ===========================================================================

// This group locks a PORT CONSENSUS, not Java parity. The Java agent has no
// equivalent of any of it: shutdown there is per-component - each DataSender
// releases its own executor, GrpcDataSender.release (GrpcDataSender.java:71-76)
// awaiting three seconds for that executor alone - with no wall-clock bound on
// the teardown as a whole and no report of what was still running when it gave
// up. The Go and C++ agents both bound it and both name the stragglers, so the
// contract is theirs to keep in step.

// Test_javaParityLock_ShutdownDeadline locks the bound on the blocking phase
// of shutdown (shutdownTimeout, agent.go:557). Past it the workers are
// abandoned and Shutdown returns: a collector outage must not keep the host
// process alive, and the queue drain each worker is doing cannot be bounded
// on its own. Test_agent_ShutdownDeadline (agent_test.go) exercises the wait
// end to end against a wedged worker.
func Test_javaParityLock_ShutdownDeadline(t *testing.T) {
	assert.Equal(t, 3*time.Second, shutdownTimeout,
		"3s bounds the whole teardown; Java bounds only each sender's executor")
}

// Test_javaParityLock_ShutdownIsIdempotent locks that the teardown runs once
// however many callers reach Shutdown, sequentially or concurrently
// (shutdownOnce, agent.go:725). Not a formality: shutdownAgent closes the
// span queue, which closes a channel, so a second run would panic with "close
// of closed channel" and take the host process down on the way out - exactly
// what an agent must never do. Run this one under -race as well:
// Test_agent_ShutdownIsSerialized (agent_test.go) covers the ordering half,
// that a concurrent second caller waits for the first rather than returning
// into a half-torn-down agent.
func Test_javaParityLock_ShutdownIsIdempotent(t *testing.T) {
	agent := newTestAgent(defaultConfig())

	const callers = 8
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			agent.Shutdown()
		}()
	}
	wg.Wait()
	agent.Shutdown()

	assert.Equal(t, phaseStopped, agent.enable.current(), "the teardown completed")
	assert.True(t, agent.spanQueue.closed.Load(), "the span queue was closed, exactly once")
}

// Test_javaParityLock_ShutdownNamesStragglers locks that a deadline overrun
// reports the workers still running, by the names the worker table gave them
// (runningWorkerNames, agent.go:543; the report itself is agent.go:810-813).
// "shutdown timeout exceeded" without the names is not actionable in a host
// process, and the names are the log contract the troubleshooting guide
// reads. The list has to be exactly the workers whose supervisor has not
// exited - not every declared worker, and not a count.
//
// Test_agent_ShutdownTimeoutNamesRunningWorkers and
// Test_agent_ShutdownInTimeLogsNoWorkerNames (agent_test.go) drive the whole
// shutdown to produce the line; locked here is the reporting rule itself.
func Test_javaParityLock_ShutdownNamesStragglers(t *testing.T) {
	agent := newTestAgent(defaultConfig())

	exited := &workerState{name: "ping", done: make(chan struct{})}
	stuck := &workerState{name: "send stats", done: make(chan struct{})}
	stuck.running.Store(true)
	agent.workerStates = []*workerState{exited, stuck}

	assert.Equal(t, []string{"send stats"}, agent.runningWorkerNames(),
		"only the workers that outlived the deadline are named")

	stuck.running.Store(false)
	assert.Empty(t, agent.runningWorkerNames(), "a drain that finished in time names nobody")
}

// Test_javaParityLock_WorkerTableIsTheSingleSourceOfTruth locks that the
// worker table (workerTable, agent.go:466) is the only declaration of the
// agent's goroutines: startWorkers (agent.go:500-518) spawns exactly the
// entries whose predicate holds, gives each one a state slot, and counts each
// one into workerWg right before its go statement - so the drain, the
// straggler report and the goroutine set cannot disagree. The hand-counted
// Add this replaced drifted from the go statements in both directions, and
// the compiler caught neither: too large made every Shutdown wait out its
// full deadline, too small panicked the WaitGroup.
//
// Test_agent_startWorkersCountMatchesTable (agent_test.go) locks the
// workerWg count against the same table. Locked here is the state slice -
// one slot per active entry, named by the table, nothing for an inactive one -
// since that slice is what runningWorkerNames reports from.
func Test_javaParityLock_WorkerTableIsTheSingleSourceOfTruth(t *testing.T) {
	for _, spanBatch := range []bool{true, false} {
		for _, refreshInterval := range []int{0, 1000} {
			agent := newTestAgent(workerTableConfig(spanBatch, refreshInterval))
			table := agent.workerTable()
			active := activeWorkerNames(table)

			// The table's own bodies need a collector; keep its names and
			// predicates and park each body on the stop signal instead.
			stop := agent.stopSignal().Done()
			stubs := make([]worker, len(table))
			for i, w := range table {
				stubs[i] = worker{name: w.name, when: w.when, body: func() { <-stop }}
			}
			agent.startWorkers(stubs)

			names := make([]string, 0, len(agent.workerStates))
			for _, st := range agent.workerStates {
				names = append(names, st.name)
			}
			assert.Equal(t, active, names,
				"one state slot per active table entry, in table order (span batch %v, refresh %d)",
				spanBatch, refreshInterval)
			for _, w := range table {
				if !w.when() {
					assert.NotContains(t, names, w.name, "an inactive entry gets no slot")
				}
			}

			agent.signalShutdown()
			assert.True(t, waitTimeout(&agent.workerWg, shutdownTimeout),
				"every started worker releases the workerWg slot startWorkers added for it")
		}
	}
}

// ===========================================================================
// Group 17 - metadata retry budget and rejection policy (port consensus)
// ===========================================================================

// The retry BUDGET is Java's: MetadataGrpcDataSender retries a failed send up
// to profiler.transport.grpc.metadata.sender.retry.max.count (3) times,
// retry.delay.millis (1000) apart, and queues new metadata on an executor
// queue of metadata.sender.executor.queue.size (1000) entries. Both ports keep
// the same three numbers, and both bound the retry schedule separately at the
// size of the new-metadata queue (Java's HashedWheelTimer is unbounded).
//
// The rejection POLICY is a port consensus that diverges from Java: a reply
// with PResult.success=false is NOT retried (RetryResponseStreamObserver
// retries it like a transport failure). The item is dropped and its cache
// entry released after one retry delay (metaRejected in metaVerdictOf, the
// release-only entry in agent.metaRetry). Rationale in doc/java_parity.md
// ("Retrying a rejected metadata send"). The behaviour itself is pinned by the
// metaVerdictOf and sendMetadataOnce tests; the C++ suite mirrors both halves
// as its group 17 (MetadataRetryBudget).
func Test_javaParityLock_MetadataRetryBudget(t *testing.T) {
	assert.Equal(t, 3, metaRetryMaxAttempts, "Java profiler.transport.grpc.metadata.sender.retry.max.count")
	assert.Equal(t, time.Second, metaRetryDelay, "Java profiler.transport.grpc.metadata.sender.retry.delay.millis")
	assert.Equal(t, 1000, metaRetryQueueSize,
		"port consensus: the retry schedule is bounded like the new-metadata queue (Java metadata.sender.executor.queue.size)")
	assert.Equal(t, 1000, defaultMetaQueueSize, "Java profiler.transport.grpc.metadata.sender.executor.queue.size")
	rejected := metaResult(&pb.PResult{Success: false, Message: "no"}, nil)
	assert.Equal(t, metaRejected, metaVerdictOf(rejected, 1),
		"port consensus: a PResult.success=false reply is not retried")
}
