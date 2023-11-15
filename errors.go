package pinpoint

import (
	"runtime"
	"strings"
	"time"
)

type errorWithCallStack struct {
	errorMessage string
	errorTime    time.Time
	callstack    []uintptr
}

func dumpCallStack(errMsg string, depth int) *errorWithCallStack {
	pcs := make([]uintptr, depth+3)
	n := runtime.Callers(3, pcs)

	return &errorWithCallStack{
		errorMessage: errMsg,
		errorTime:    time.Now(),
		callstack:    pcs[0:n],
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
