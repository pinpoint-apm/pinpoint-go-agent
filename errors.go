package pinpoint

import (
	"runtime"
	"strings"
	"time"
)

type errorCallStack struct {
	error
	errorTime time.Time
	callstack []uintptr
}

func genCallStack(err error) *errorCallStack {
	var pcs [32]uintptr
	n := runtime.Callers(4, pcs[:])

	return &errorCallStack{
		err,
		time.Now(),
		pcs[0:n],
	}
}

func (e *errorCallStack) stackTrace() []frame {
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
	fn := runtime.FuncForPC(pc)
	if fn != nil {
		file, line = fn.FileLine(pc)
		moduleName, funcName = splitName(fn.Name())
	}

	return frame{moduleName, funcName, file, int32(line)}
}

func splitName(fullName string) (string, string) {
	lastIdx := strings.LastIndex(fullName, ".")
	return fullName[:lastIdx], fullName[lastIdx+1:]
}
