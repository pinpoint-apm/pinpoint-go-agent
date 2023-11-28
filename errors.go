package pinpoint

import (
	"runtime"
	"strings"
	"time"
	"unsafe"

	pkgError "github.com/pkg/errors"
)

type pkgErrorStackTracer interface {
	StackTrace() pkgError.StackTrace
}

type errorWithCallStack struct {
	errorMessage string
	errorTime    time.Time
	callstack    []uintptr
}

func traceCallStack(e error, depth int) *errorWithCallStack {
	var callstack []uintptr

	if err, ok := e.(pkgErrorStackTracer); ok {
		st := err.StackTrace()
		callstack = *(*[]uintptr)(unsafe.Pointer(&st))
	} else {
		pcs := make([]uintptr, depth+3)
		n := runtime.Callers(3, pcs)
		callstack = pcs[0:n]
	}

	return &errorWithCallStack{
		errorMessage: e.Error(),
		errorTime:    time.Now(),
		callstack:    callstack,
	}
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
