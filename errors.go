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

func (span *span) findError(err error) *exception {
	for _, chain := range span.errorChains {
		if chain.callstack.err == err {
			return chain
		}
	}
	return nil
}

// maxCauserDepth bounds how far the Cause() chain of a user error is walked.
// The chain comes from an arbitrary user implementation: one whose Cause()
// returns the error itself, or cycles back to an ancestor, would otherwise
// hang the request goroutine inside SetError.
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

	for e, depth := err, 0; e != nil && depth < maxCauserDepth; depth++ {
		e = nextCause(e)
		if ec := span.findError(e); ec != nil {
			return ec.exceptionId, true
		}
	}

	// Only a new chain is rate limited, like Java's DefaultExceptionRecorder
	// asking ExceptionChainSampler.isNewSampled() just for a new id: a denied
	// request yields the DISABLED state, recording nothing. The id is minted
	// after the permit is granted, so a denial does not burn one.
	if l := span.cfg.newExceptionLimiter; l != nil && !l.Allow() {
		return noExceptionChainId, false
	}
	return span.agent.exceptionIdGen.Add(1), true
}

// addCauserCallStack records the causes of err under the same exception id,
// numbered depth 1..n in chain order like Java's ExceptionWrapperFactory
// (err itself is depth 0). A cause already recorded on this span ends the
// walk: its own chain is on the wire already.
func (span *span) addCauserCallStack(err error, eid int64, errorTime time.Time) {
	e := err
	for depth := 1; depth < maxCauserDepth; depth++ {
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
	}
	return eid
}
