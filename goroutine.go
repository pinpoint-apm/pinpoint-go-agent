package pinpoint

import (
	"bufio"
	"bytes"
	"io"
	"math"
	"reflect"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"unsafe"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/asm"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf"
)

var (
	goIdOffset uintptr
	stateMap   map[string]pb.PThreadState
)

func initGoroutine() {
	goIdOffset = getOffset()

	stateMap = make(map[string]pb.PThreadState, 0)
	stateMap[""] = pb.PThreadState_THREAD_STATE_UNKNOWN
	stateMap["???"] = pb.PThreadState_THREAD_STATE_UNKNOWN
	stateMap["idle"] = pb.PThreadState_THREAD_STATE_NEW
	stateMap["runnable"] = pb.PThreadState_THREAD_STATE_RUNNABLE
	stateMap["running"] = pb.PThreadState_THREAD_STATE_RUNNABLE
	stateMap["syscall"] = pb.PThreadState_THREAD_STATE_RUNNABLE
	stateMap["copystack"] = pb.PThreadState_THREAD_STATE_RUNNABLE
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

func (g *goroutine) addLine(line []byte) {
	g.buf.Write(line)
	g.buf.WriteByte('\n')
}

func (g *goroutine) threadState() pb.PThreadState {
	if s, ok := stateMap[g.state]; ok {
		return s
	}
	return pb.PThreadState_THREAD_STATE_WAITING
}

func newGoroutine(id int64, state string, line []byte) *goroutine {
	g := &goroutine{
		id:     id,
		header: goroutineHeaderPrefix + strconv.FormatInt(id, 10),
		state:  strings.TrimSpace(strings.Split(state, ",")[0]),
		buf:    &bytes.Buffer{},
	}
	g.addLine(line)
	return g
}

// goroutineHeaderPrefix starts every goroutine block of a debug=2 profile.
const goroutineHeaderPrefix = "goroutine "

// parseGoroutineHeader reads "goroutine <id> [<state>]:", the block header
// runtime.Stack writes, and reports false for any other line. The state is
// everything between the brackets, "select, 3 minutes" included; the caller
// keeps the first word. By hand rather than with a regexp: the dump has one
// header per goroutine of the process, and ^goroutine\s+(\d+)\s+\[(.*)\]:$
// took a third of the parse on a 10k-goroutine dump.
func parseGoroutineHeader(line []byte) (id int64, state []byte, ok bool) {
	if !bytes.HasPrefix(line, []byte(goroutineHeaderPrefix)) {
		return 0, nil, false
	}
	rest := line[len(goroutineHeaderPrefix):]
	sp := bytes.IndexByte(rest, ' ')
	if sp < 1 {
		return 0, nil, false
	}
	for _, c := range rest[:sp] {
		if c < '0' || c > '9' {
			return 0, nil, false
		}
		if id > (math.MaxInt64-int64(c-'0'))/10 {
			return 0, nil, false // more digits than an id can have
		}
		id = id*10 + int64(c-'0')
	}
	rest = rest[sp+1:]
	if len(rest) < 3 || rest[0] != '[' || rest[len(rest)-2] != ']' || rest[len(rest)-1] != ':' {
		return 0, nil, false
	}
	return id, rest[1 : len(rest)-2], true
}

type goroutineDump struct {
	goroutines []*goroutine
}

func (gd *goroutineDump) add(g *goroutine) {
	gd.goroutines = append(gd.goroutines, g)
}

// indexByHeader indexes only the requested names, keeping the full/light dump
// parse path unchanged while reducing selection from M scans of A goroutines
// to one scan plus M lookups.
func (gd *goroutineDump) indexByHeader(names []string) map[string]*goroutine {
	index := make(map[string]*goroutine, len(names))
	for _, name := range names {
		index[name] = nil
	}
	if len(index) == 0 {
		return index
	}

	remaining := len(index)
	for _, g := range gd.goroutines {
		if selected, requested := index[g.header]; requested && selected == nil {
			index[g.header] = g
			remaining--
			if remaining == 0 {
				break
			}
		}
	}
	return index
}

func newGoroutineDump() *goroutineDump {
	return &goroutineDump{
		goroutines: []*goroutine{},
	}
}

func dumpGoroutine(agent *agent) *goroutineDump {
	return dumpGoroutineProfile(agent, func(w io.Writer) error {
		if p := pprof.Lookup("goroutine"); p != nil {
			return p.WriteTo(w, 2)
		}
		return nil
	})
}

func dumpGoroutineProfile(agent *agent, write func(io.Writer) error) (dump *goroutineDump) {
	var b bytes.Buffer

	defer func() {
		if e := recover(); e != nil {
			Log("cmd").Errorf("profile goroutine: %v", e)
			dump = nil
		}
	}()

	if err := write(&b); err != nil {
		Log("cmd").Errorf("profile goroutine: %v", err)
		return nil
	}

	dump = parseProfile(&b, agent)
	return
}

// parseProfile keeps the goroutines that carry a span (realTimeActiveSpan)
// and nothing else: the dump holds every goroutine of the process, and the
// handful being traced is all the command reports. An untracked goroutine's
// block is skipped line by line without a goroutine struct or a buffer, where
// collecting it first copied the whole dump a second time only to drop it.
func parseProfile(r io.Reader, agent *agent) *goroutineDump {
	dump := newGoroutineDump()
	// g collects the stack lines of a tracked goroutine; inBlock says a block
	// is open, tracked or not, so a header is looked for only between blocks.
	var g *goroutine
	inBlock := false

	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		// Bytes, not Text: Text copies every line into a string, and the
		// lines of untracked goroutines are looked at and dropped.
		line := scanner.Bytes()
		if inBlock {
			if len(line) == 0 {
				inBlock, g = false, nil
			} else if g != nil {
				g.addLine(line)
			}
			continue
		}
		id, state, ok := parseGoroutineHeader(line)
		if !ok {
			continue
		}
		inBlock = true
		if v, ok := agent.realTimeActiveSpan.Load(id); ok {
			g = newGoroutine(id, string(state), line)
			g.span = v.(*activeSpanInfo)
			dump.add(g)
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
		return goIdFromG()
	} else {
		return goIdFromDump()
	}
}

var prefix = len("goroutine ")

func goIdFromDump() int64 {
	b := make([]byte, 64)
	b = b[prefix:runtime.Stack(b, false)]
	idStr := string(b[:bytes.IndexByte(b, ' ')])
	if IsDebugLogLevelEnabled() {
		Log("cmd").Debugf("idStr: '%s'", idStr)
	}
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
