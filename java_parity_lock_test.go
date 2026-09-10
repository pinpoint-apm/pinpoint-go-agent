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
// Every assertion in this file is a value or an algorithm that the Java agent
// (agent-module/profiler), the C++ agent and this agent were verified to agree
// on. They are locked here so a later change has to state its intent instead
// of drifting one agent away from the other two: a failure means either the
// change is wrong, or all three implementations and doc/java_parity.md move
// together.
//
// The C++ agent keeps the same suite at test/test_java_parity_lock.cpp, group
// for group, and doc/java_parity.md ("Locked parity invariants") is the table
// that ties both to the Java reference. Add a group here only when the same
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

package pinpoint

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ===========================================================================
// Group 1 - SQL normalization state machine
// ===========================================================================

// javaParitySqlCase is one golden case. The expectations are the Java agent's
// (commons-profiler ParserContext.parse via DefaultSqlNormalizer's no-arg
// constructor, i.e. removeComments=false) and are asserted verbatim by the C++
// agent's SqlTest.JavaParityGoldenCases.
type javaParitySqlCase struct {
	name       string
	sql        string
	normalized string
	params     string
	// paramsUnsplittable marks a case whose param string cannot be split back
	// into one entry per placeholder: an unterminated literal writes its
	// content into param without emitting a placeholder, so the counts do not
	// line up. Java has the same quirk; only the placeholder-index invariant
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
// (SQL.RemoveComments=true, Java's profiler.jdbc.removecomments): comments are
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
// ',,' that a literal containing a comma produces. The C++ agent keeps the
// same helper in test/test_sql.cpp.
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

// ===========================================================================
// Group 2 - span event depth / sequence numbering
// ===========================================================================

// Test_javaParityLock_SpanEventLimitDefaults locks the call stack limits. Java:
// profiler.callstack.max.depth=64, profiler.callstack.max.sequence=5000,
// profiler.io.buffering.buffersize=20 (DefaultInstrumentConfig, pinpoint-root.config).
func Test_javaParityLock_SpanEventLimitDefaults(t *testing.T) {
	assert.Equal(t, 64, defaultEventDepth, "Java profiler.callstack.max.depth")
	assert.Equal(t, 5000, defaultEventSequence, "Java profiler.callstack.max.sequence")
	assert.Equal(t, 20, defaultEventChunkSize, "Java profiler.io.buffering.buffersize")
}

// Test_javaParityLock_SpanEventLimitFloors locks the floors this agent and the
// C++ agent add on top of Java (Java has no floor). They exist so a
// misconfigured limit cannot make the call stack unusable; the values must stay
// equal between the two ports.
func Test_javaParityLock_SpanEventLimitFloors(t *testing.T) {
	assert.Equal(t, 2, minEventDepth, "C++ defaults MIN_SPAN_MAX_EVENT_DEPTH")
	assert.Equal(t, 4, minEventSequence, "C++ defaults MIN_SPAN_MAX_EVENT_SEQUENCE")
}

// javaOverflowDecision is the overflow predicate all three agents implement.
// Java: DefaultCallStack.isOverflow, `maxDepth < index || maxSequence <= sequence`
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

// ===========================================================================
// Group 3 - span chunk serialization
// ===========================================================================

func parityEvent(sequence, depth int32, startTime int64) *spanEvent {
	return &spanEvent{sequence: sequence, depth: depth, startTime: startTime}
}

// Test_javaParityLock_ChunkKeyTimeAndStartElapsed locks the two values the
// collector uses to rebuild absolute event times. Java: GrpcSpanProcessorV2 -
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
// sequence before it is serialized. Java sorts with SEQUENCE_COMPARATOR; the
// C++ agent keeps the order on insert. The delta encoding above is only
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

// Test_javaParityLock_Sentinels locks the reserved values. Java: SpanId.NULL
// is -1 and an async id of 0 means "no async context"; both agents skip these
// when they draw an id, so a drawn value can never be mistaken for "absent".
func Test_javaParityLock_Sentinels(t *testing.T) {
	assert.Equal(t, int64(-1), int64(noneSpanId), "Java SpanId.NULL")
	assert.Equal(t, int32(0), int32(noneAsyncId), "Java: asyncId 0 means no async context")
}

// Test_javaParityLock_GeneratedSpanIdIsNeverTheSentinel locks that a drawn span
// id is never SpanId.NULL. Java redraws in the same situation
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
// Java's Header enum (commons). A rename on one side silently breaks tracing
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
// `agentId^startTime^sequence` (Java TransactionIdUtils) and the round trip
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
// Java validates the agent id character class (IdValidateUtils) and stops at the
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
// "s0" turns sampling off. Java: SamplingFlagUtils.isSamplingFlag - anything
// else, "s1" or an absent header included, is sampled.
func Test_javaParityLock_SampledHeaderEncoding(t *testing.T) {
	const samplingFlagFalse = "s0"

	assert.Equal(t, "s0", samplingFlagFalse, "the off value is exactly \"s0\"")
	for _, v := range []string{"s1", "S0", "", "0", "false", "s00", " s0"} {
		assert.NotEqual(t, samplingFlagFalse, v, "%q must not disable sampling", v)
	}
}

// ===========================================================================
// Group 6 - sampling formulas
// ===========================================================================

// Test_javaParityLock_CountingSamplerPhase locks the counting sampler's phase.
// Java CountingSampler tests the pre-increment value, so the first request of
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

// Test_javaParityLock_CountingSamplerEdgeRates locks the rates Java hands to
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

// Test_javaParityLock_PercentSamplerWindow locks the admission window. Java
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

// Test_javaParityLock_PercentSamplerRateTruncation locks the truncation Java
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

// ===========================================================================
// Group 7 - URI histogram layout
// ===========================================================================

// Test_javaParityLock_UrlStatHistogramBuckets locks the eight bucket bounds
// against Java's UriStatHistogramBucket.Layout. The collector stores the counts
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

// Test_javaParityLock_UrlStatWindow locks the tick size and the completed-queue
// cap. Java: AsyncQueueingUriStatStorage buckets on a 30s TickClock and keeps
// four snapshots.
func Test_javaParityLock_UrlStatWindow(t *testing.T) {
	assert.Equal(t, 30*time.Second, urlStatCollectInterval, "Java TickClock interval")
	assert.Equal(t, 4, maxCompletedUrlStatSnapshots, "Java snapshotQueue capacity / C++ kMaxCompletedSnapshots")
}

// Test_javaParityLock_UrlStatEmptyHistogram locks that an all-zero histogram
// reports empty, which is what keeps an empty PUriHistogram off the wire. Java
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
// records URL stats without a URI template against Java's URITemplate.NULL_URI
// ("/NULL"), which the C++ agent copies verbatim (URL_STAT_UNKNOWN,
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

// ===========================================================================
// Group 8 - active trace histogram layout
// ===========================================================================

// Test_javaParityLock_ActiveTraceHistogram locks the four active-trace slots
// against Java's NORMAL histogram schema (BaseHistogramSchema: 1000/3000/5000ms
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

// Test_javaParityLock_TransactionCounters locks that all six counters Java's
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

// Test_javaParityLock_TruncationFormat locks the abbreviation Java's
// StringUtils.abbreviate writes: the value cut to the limit followed by
// "...(original length)". The web tier shows the marker as-is, so the format is
// part of the contract.
func Test_javaParityLock_TruncationFormat(t *testing.T) {
	assert.Equal(t, "short", abbreviateString("short", 10), "a value within the limit is untouched")
	assert.Equal(t, "0123456789", abbreviateString("0123456789", 10), "exactly at the limit is untouched")
	assert.Equal(t, "0123456789...(11)", abbreviateString("0123456789A", 10), "the marker carries the original length")
}

// Test_javaParityLock_TruncationCutsOnARuneBoundary locks the UTF-8 guard both
// ports add on top of Java: protobuf rejects invalid UTF-8 at marshal time, so
// a mid-rune cut would fail the whole span or metadata send carrying it.
func Test_javaParityLock_TruncationCutsOnARuneBoundary(t *testing.T) {
	// "가" is three bytes; a limit of 4 lands inside the second rune.
	got := abbreviateString("가가가", 4)
	assert.True(t, strings.HasPrefix(got, "가"))
	assert.Equal(t, "가...(9)", got)
}

// Test_javaParityLock_MessageLimits locks the two message limits. Java:
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
// and maxHeaderListSize follow Java's ClientOption; the C++ agent leaves those
// three at the C-core defaults, which doc/java_parity.md records. The idle
// timeout is deliberately not locked: all three agents disable idling, but
// Java's disable value is 30 days and this agent's is grpc-go's 0, so only the
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
// The retry interval deliberately differs from Java's effective 300000ms
// (profiler.agentInfo.send.retry.interval): registration gates tracing in both
// ports, so it has to retry far more often. doc/java_parity.md records that.
func Test_javaParityLock_AgentInfoSchedule(t *testing.T) {
	assert.Equal(t, 24*60*60*1000, defaultAgentInfoRefreshInterval, "Java AgentInfoSender refresh interval")
	assert.Equal(t, 3, defaultAgentInfoMaxTryPerAttempt, "Java AgentInfoSender maxTryPerAttempt")
	assert.Equal(t, 3000, defaultAgentInfoSendRetryInterval, "matches the C++ agent, not Java's effective 300000ms - see doc/java_parity.md")
}
