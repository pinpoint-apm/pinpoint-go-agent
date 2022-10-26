package pinpoint

import (
	"bufio"
	"bytes"
	"io"
	"reflect"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"unsafe"

	"github.com/pinpoint-apm/pinpoint-go-agent/asm"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/sirupsen/logrus"
)

var (
	goIdOffset uintptr
	stateMap   map[string]pb.PThreadState
)

func initGoroutine() {
	goIdOffset = getOffset()
	if goIdOffset > 0 {
		Log("cmd").Infof("goroutine id from runtime.g (offset = %d)", goIdOffset)
	} else {
		Log("cmd").Infof("goroutine id from stack dump")
	}

	stateMap = make(map[string]pb.PThreadState, 0)
	stateMap[""] = pb.PThreadState_THREAD_STATE_UNKNOWN
	stateMap["???"] = pb.PThreadState_THREAD_STATE_UNKNOWN
	stateMap["idle"] = pb.PThreadState_THREAD_STATE_NEW
	stateMap["runnable"] = pb.PThreadState_THREAD_STATE_RUNNABLE
	stateMap["running"] = pb.PThreadState_THREAD_STATE_RUNNABLE
	stateMap["syscall"] = pb.PThreadState_THREAD_STATE_RUNNABLE
	stateMap["copystack"] = pb.PThreadState_THREAD_STATE_RUNNABLE
	stateMap["preempted"] = pb.PThreadState_THREAD_STATE_RUNNABLE
	stateMap["dead"] = pb.PThreadState_THREAD_STATE_TERMINATED
	stateMap["dumping heap"] = pb.PThreadState_THREAD_STATE_BLOCKED
	stateMap["garbage collection"] = pb.PThreadState_THREAD_STATE_BLOCKED
	stateMap["garbage collection scan"] = pb.PThreadState_THREAD_STATE_BLOCKED
	stateMap["force gc (idle)"] = pb.PThreadState_THREAD_STATE_BLOCKED
	stateMap["trace reader (blocked)"] = pb.PThreadState_THREAD_STATE_BLOCKED
	stateMap["preempted"] = pb.PThreadState_THREAD_STATE_BLOCKED
	stateMap["debug call"] = pb.PThreadState_THREAD_STATE_BLOCKED
	stateMap["stopping the world"] = pb.PThreadState_THREAD_STATE_BLOCKED
	// the rest of the state are considered pb.PThreadState_THREAD_STATE_WAITING
	// refer https://github.com/golang/go/blob/master/src/runtime/runtime2.go: waitReasonStrings
}

type goroutine struct {
	id     int64
	header string
	state  string
	buf    *bytes.Buffer
	span   *activeSpanInfo
}

func (g *goroutine) addLine(line string) {
	g.buf.WriteString(line)
	g.buf.WriteString("\n")
}

func (g *goroutine) threadState() pb.PThreadState {
	if s, ok := stateMap[g.state]; ok {
		return s
	}
	return pb.PThreadState_THREAD_STATE_WAITING
}

func (g *goroutine) stackTrace() []string {
	return append(make([]string, 0), g.buf.String())
}

func newGoroutine(idStr string, state string, line string) *goroutine {
	if id, err := strconv.Atoi(idStr); err == nil {
		g := &goroutine{
			id:     int64(id),
			header: "goroutine " + idStr,
			state:  strings.TrimSpace(strings.Split(state, ",")[0]),
			buf:    &bytes.Buffer{},
		}
		g.addLine(line)
		return g
	} else {
		Log("cmd").Errorf("convert goroutine id: %v", err)
	}
	return nil
}

type goroutineDump struct {
	goroutines []*goroutine
}

func (gd *goroutineDump) add(g *goroutine) {
	gd.goroutines = append(gd.goroutines, g)
}

func (gd *goroutineDump) search(s string) *goroutine {
	for _, g := range gd.goroutines {
		if g.header == s {
			return g
		}
	}

	return nil
}

func newGoroutineDump() *goroutineDump {
	return &goroutineDump{
		goroutines: []*goroutine{},
	}
}

func dumpGoroutine() (dump *goroutineDump) {
	var b bytes.Buffer
	buf := bufio.NewWriter(&b)

	defer func() {
		if e := recover(); e != nil {
			Log("cmd").Errorf("profile goroutine: %v", e)
			dump = nil
		}
	}()

	if p := pprof.Lookup("goroutine"); p != nil {
		if err := p.WriteTo(buf, 2); err != nil {
			Log("cmd").Errorf("profile goroutine: %v", err)
			return nil
		}
	}

	dump = parseProfile(&b)
	return
}

func parseProfile(r io.Reader) *goroutineDump {
	dump := newGoroutineDump()
	var g *goroutine

	scanner := bufio.NewScanner(r)
	re := regexp.MustCompile(`^goroutine\s+(\d+)\s+\[(.*)\]:$`)

	for scanner.Scan() {
		line := scanner.Text()
		if g == nil {
			if match := re.FindAllStringSubmatch(line, 1); match != nil {
				if g = newGoroutine(match[0][1], match[0][2], line); g != nil {
					if v, ok := realTimeActiveSpan.Load(g.id); ok {
						g.span = v.(*activeSpanInfo)
						dump.add(g)
					}
				}
			}
		} else {
			if line == "" {
				g = nil
			} else {
				g.addLine(line)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		Log("cmd").Errorf("scan goroutine profile: %v", err)
		return nil
	}

	return dump
}

func curGoroutineID() int64 {
	if goIdOffset > 0 {
		id := goIdFromG()
		if logger.Level >= logrus.DebugLevel {
			dumpId := goIdFromDump()
			if id != dumpId {
				Log("cmd").Errorf("different goID: getg= %d, dump= %d", id, dumpId)
			}
		}
		return id
	} else {
		return goIdFromDump()
	}
}

var prefix = len("goroutine ")

func goIdFromDump() int64 {
	b := make([]byte, 64)
	b = b[prefix:runtime.Stack(b, false)]
	idStr := string(b[:bytes.IndexByte(b, ' ')])
	Log("cmd").Debugf("idStr: '%s'", idStr)
	id, _ := strconv.ParseInt(idStr, 10, 64)
	return id
}

func getOffset() uintptr {
	if typ := typeRuntimeG(); typ != nil {
		if f, ok := typ.FieldByName("goid"); ok {
			return f.Offset
		}
	}
	return 0
}

func typeRuntimeG() reflect.Type {
	sections, offsets := typelinks()
	//load go types
	for i, base := range sections {
		for _, offset := range offsets[i] {
			typeAddr := add(base, uintptr(offset), "")
			typ := reflect.TypeOf(*(*interface{})(unsafe.Pointer(&typeAddr)))
			if typ.Kind() == reflect.Ptr && typ.Elem().String() == "runtime.g" {
				return typ.Elem()
			}
		}
	}
	return nil
}

//go:linkname typelinks reflect.typelinks
func typelinks() (sections []unsafe.Pointer, offset [][]int32)

//go:linkname add reflect.add
func add(p unsafe.Pointer, x uintptr, whySafe string) unsafe.Pointer

func goIdFromG() int64 {
	return *(*int64)(unsafe.Pointer(uintptr(asm.Getg()) + goIdOffset))
}
