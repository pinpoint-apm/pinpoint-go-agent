package pinpoint

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"time"
	"unsafe"

	pkgError "github.com/pkg/errors"
)

type pkgErrorStackTracer interface {
	StackTrace() pkgError.StackTrace
}

type causer interface {
	Cause() error
}

type errorWithCallStack struct {
	err       error
	errorTime time.Time
	callstack []uintptr
}

// errorTypeName is the Go counterpart of Java's exception class name: the
// error's dynamic type with any pointer stripped, e.g. "errors.withStack".
func errorTypeName(err error) string {
	t := reflect.TypeOf(err)
	if t == nil {
		return "error"
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.String()
}

// nextCause steps one link down an error chain, preferring pkg/errors'
// Cause() and falling back to the standard Unwrap() (fmt.Errorf %w).
// A multi-unwrap error (errors.Join, Unwrap() []error) contributes only its
// first element: the Pinpoint exception chain is a single line of causes, as
// Java's Throwable.getCause() is, and has no way to report a tree.
func nextCause(err error) error {
	if c, ok := err.(causer); ok {
		return c.Cause()
	}
	if cause := errors.Unwrap(err); cause != nil {
		return cause
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		if errs := joined.Unwrap(); len(errs) > 0 {
			return errs[0]
		}
	}
	return nil
}

// errorCallStack is the stack the error carries itself, nil when it has none.
func errorCallStack(err error) []uintptr {
	if pkgErr, ok := err.(pkgErrorStackTracer); ok {
		st := pkgErr.StackTrace()
		return *(*[]uintptr)(unsafe.Pointer(&st))
	}
	return nil
}

func (e *errorWithCallStack) stackTrace() []frame {
	f := make([]frame, len(e.callstack))
	for i := 0; i < len(f); i++ {
		f[i] = newFrame(e.callstack[i])
	}
	return f
}

type frame struct {
	moduleName string
	funcName   string
	file       string
	line       int32
}

func newFrame(f uintptr) frame {
	moduleName := "unknown"
	funcName := "unknown"
	file := "unknown"
	line := 0

	pc := uintptr(f) - 1
	if fn := runtime.FuncForPC(pc); fn != nil {
		file, line = fn.FileLine(pc)
		moduleName, funcName = splitName(fn.Name())
	}

	return frame{moduleName, funcName, file, int32(line)}
}

func splitName(fullName string) (string, string) {
	lastIdx := strings.LastIndex(fullName, ".")
	if lastIdx < 0 {
		// fullName[:-1] would panic. Ordinary Go frames always carry a dot,
		// but the frames can come from a user error type's StackTrace(), and
		// this runs on the recover-less metadata sender goroutine.
		return "unknown", fullName
	}
	return fullName[:lastIdx], fullName[lastIdx+1:]
}

// sameError reports whether a and b are the same error value. Comparing two
// interfaces with == panics when both hold the same uncomparable dynamic type
// - a slice-, map- or func-based error such as validator.ValidationErrors -
// and this runs on the request goroutine for any error handed to SetError, so
// such a pair is reported as distinct instead of taken to the comparison.
func sameError(a, b error) bool {
	if ta := reflect.TypeOf(a); ta != reflect.TypeOf(b) || (ta != nil && !ta.Comparable()) {
		return false
	}
	return a == b
}

func (span *span) findError(err error) *exception {
	for _, chain := range span.errorChains {
		if sameError(chain.callstack.err, err) {
			return chain
		}
	}
	return nil
}

// maxCauserDepth is the hard ceiling on how far the Cause() chain of a user
// error is walked, whatever Error.MaxChainDepth asks for. The chain comes from
// an arbitrary user implementation: one whose Cause() returns the error
// itself, or cycles back to an ancestor, would otherwise hang the request
// goroutine inside SetError.
const maxCauserDepth = 64

// noExceptionChainId is returned instead of a chain id when the rate limiter
// denies a new chain. Real ids come from exceptionIdGen.Add(1) and so start at
// 1, as Java's ExceptionChainSampler.INITIAL_EXCEPTION_ID does: 0 cannot
// collide with one.
const noExceptionChainId = 0

func (span *span) getExceptionChainId(err error) (int64, bool) {
	if ec := span.findError(err); ec != nil {
		return ec.exceptionId, false
	}

	// A cause already recorded joins its chain only when it is that chain's
	// head (depth 0), as Java's ExceptionRecordingState.stateOf compares the
	// new throwable's cause chain with the previously recorded throwable
	// alone. A hit on an inner link - a sentinel like io.EOF wrapped again
	// from another call site - is not a join: the recorded head is not a
	// cause of err, so shifting it below err would misorder the chain.
	//
	// A chain the limiter refused is sticky the same way: err or a cause of it
	// being the refused head is Java's CONTINUED state, which reuses the stored
	// DISABLED state rather than asking the sampler again. Without this, every
	// later link of a refused chain recorded from another span event is charged
	// as a new chain, and under an error burst those refusals crowd out chains
	// that Java would have admitted.
	refused := span.isRefusedChainHead(err)
	for e, depth := err, 0; e != nil && depth < span.cfg.errorMaxChainDepth; depth++ {
		e = nextCause(e)
		if ec := span.findError(e); ec != nil && ec.depth == 0 {
			return ec.exceptionId, true
		}
		refused = refused || span.isRefusedChainHead(e)
	}
	if refused {
		return noExceptionChainId, false
	}

	// Only a new chain is rate limited, like Java's DefaultExceptionRecorder
	// asking ExceptionChainSampler.isNewSampled() just for a new id: a denied
	// request yields the DISABLED state, recording nothing. The id is minted
	// after the permit is granted, so a denial does not burn one.
	if l := span.cfg.newExceptionLimiter; l != nil && !l.Allow() {
		span.addRefusedChainHead(err)
		return noExceptionChainId, false
	}
	return span.agent.exceptionIdGen.Add(1), true
}

// maxRefusedChainHeads is how many refused chain heads a span remembers. A
// burst refuses chains without bound, so the latch has to forget some; 8 is
// the same order as minErrorChainEntry (10), the number of exception entries a
// span keeps at all - a chain whose head fell out of the ring has almost
// certainly lost its recorded links to the entry cap too.
const maxRefusedChainHeads = 8

// isRefusedChainHead reports whether err is one of the heads the limiter
// refused. Caller holds errorChainsLock.
func (span *span) isRefusedChainHead(err error) bool {
	if err == nil {
		return false
	}
	for _, head := range span.refusedChainHeads {
		if sameError(err, head) {
			return true
		}
	}
	return false
}

// addRefusedChainHead latches err as a refused head, evicting the oldest once
// the ring is full. Caller holds errorChainsLock.
func (span *span) addRefusedChainHead(err error) {
	if span.isRefusedChainHead(err) {
		return
	}
	if len(span.refusedChainHeads) < maxRefusedChainHeads {
		span.refusedChainHeads = append(span.refusedChainHeads, err)
		return
	}
	span.refusedChainHeads[span.refusedChainNext] = err
	span.refusedChainNext = (span.refusedChainNext + 1) % maxRefusedChainHeads
}

// addCauserCallStack records the causes of err under the same exception id,
// numbered depth 1..n in chain order like Java's ExceptionWrapperFactory
// (err itself is depth 0). A cause already recorded on this span ends the
// walk: its own chain is on the wire already.
func (span *span) addCauserCallStack(err error, eid int64, errorTime time.Time) {
	e := err
	for depth := 1; depth < span.cfg.errorMaxChainDepth; depth++ {
		if e = nextCause(e); e == nil {
			break
		}
		if !span.canAddErrorChain() || span.findError(e) != nil {
			break
		}
		span.errorChains = append(span.errorChains, &exception{
			callstack: &errorWithCallStack{
				err:       e,
				errorTime: errorTime,
				callstack: errorCallStack(e),
			},
			exceptionId: eid,
			depth:       int32(depth),
			className:   errorTypeName(e),
		})
	}
}

// traceCallStack records err (depth 0) and its cause chain on the span.
// className is the name given to SetError, or "" to use the error's type
// name; errorTime is the start time of the span event that failed. It returns
// the chain id, or noExceptionChainId when the rate limiter denied a new chain.
func (span *span) traceCallStack(err error, className string, depth int, errorTime time.Time) int64 {
	span.errorChainsLock.Lock()
	defer span.errorChainsLock.Unlock()

	// Under the lock, so the cap is judged against the entries actually
	// recorded: an unlocked pre-check let two concurrent SetError calls both
	// pass and exceed it. Before getExceptionChainId, so a refused chain does
	// not spend a Error.NewThroughput permit either.
	if !span.canAddErrorChain() {
		return noExceptionChainId
	}

	eid, newId := span.getExceptionChainId(err)
	if newId {
		callstack := errorCallStack(err)
		if callstack == nil {
			// skip runtime.Callers, traceCallStack and SetError so the stack
			// starts at the caller of SetError: exactly depth frames, no more.
			pcs := make([]uintptr, depth)
			n := runtime.Callers(3, pcs)
			callstack = pcs[0:n]
		}
		if className == "" {
			className = errorTypeName(err)
		}

		existing := len(span.errorChains)
		span.errorChains = append(span.errorChains, &exception{
			callstack: &errorWithCallStack{
				err:       err,
				errorTime: errorTime,
				callstack: callstack,
			},
			exceptionId: eid,
			className:   className,
		})
		span.addCauserCallStack(err, eid, errorTime)

		// getExceptionChainId hands back an existing id when err is a new
		// wrapper around the head of a chain already recorded. Every entry of
		// that chain is now below the links just appended, so they shift down
		// by that many: the
		// outermost error keeps depth 0, as Java's ExceptionWrapperFactory
		// numbers a chain it wraps, and no two entries share a depth - a
		// second depth 0 leaves the collector no way to order the chain.
		// A genuinely new id matches nothing here, so the loop is a no-op.
		added := int32(len(span.errorChains) - existing)
		for _, ec := range span.errorChains[:existing] {
			if ec.exceptionId == eid {
				ec.depth += added
			}
		}
	}
	return eid
}
