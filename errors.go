package pinpoint

import (
	"runtime"
)

type frame struct {
	file     string
	line     int
	funcName string
}

func newFrame(f uintptr) frame {
	file := "unknown"
	funcName := "unknown"
	line := 0

	pc := uintptr(f) - 1
	fn := runtime.FuncForPC(pc)
	if fn != nil {
		file, line = fn.FileLine(pc)
		funcName = fn.Name()
	}

	return frame{file, line, funcName}
}

type callstack []uintptr

func (cs *callstack) stackTrace() []frame {
	f := make([]frame, len(*cs))
	for i := 0; i < len(f); i++ {
		f[i] = newFrame((*cs)[i])
	}
	return f
}

func callers() *callstack {
	const depth = 32
	var pcs [depth]uintptr

	n := runtime.Callers(3, pcs[:])
	var st callstack = pcs[0:n]

	return &st
}

type withCallStack struct {
	error
	*callstack
}

func (w *withCallStack) Unwrap() error { return w.error }

func WrapError(err error) error {
	if err == nil {
		return nil
	}
	return &withCallStack{
		err,
		callers(),
	}
}
