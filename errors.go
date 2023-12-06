package pinpoint

import (
	"runtime"
	"strings"
	"sync/atomic"
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
	return fullName[:lastIdx], fullName[lastIdx+1:]
}

type errorChain struct {
	callstack   *errorWithCallStack
	exceptionId int64
}

func (span *span) findError(err error) *errorChain {
	for _, chain := range span.errorChains {
		if chain.callstack.err == err {
			return chain
		}
	}
	return nil
}

func (span *span) traceCallStack(err error, depth int) int64 {
	var callstack []uintptr
	var eid = int64(0)

	if pkgErr, ok := err.(pkgErrorStackTracer); ok {
		if ec := span.findError(err); ec != nil {
			return ec.exceptionId
		}

		for e := err; e != nil; {
			c, ok2 := e.(causer)
			if !ok2 {
				break
			}

			e = c.Cause()
			if ec := span.findError(e); ec != nil {
				eid = ec.exceptionId
				break
			}
		}

		st := pkgErr.StackTrace()
		callstack = *(*[]uintptr)(unsafe.Pointer(&st))
	} else {
		pcs := make([]uintptr, depth+3)
		n := runtime.Callers(3, pcs)
		callstack = pcs[0:n]
	}

	if eid == 0 {
		eid = atomic.AddInt64(&exceptionIdGen, 1)
	}

	chain := &errorChain{
		callstack: &errorWithCallStack{
			err:       err,
			errorTime: time.Now(),
			callstack: callstack,
		},
		exceptionId: eid,
	}
	span.errorChains = append(span.errorChains, chain)

	return eid
}
