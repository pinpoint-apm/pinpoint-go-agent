package pinpoint

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"unsafe"

	"github.com/pinpoint-apm/pinpoint-go-agent/asm"
	"github.com/sirupsen/logrus"
)

type goroutine struct {
	id     uint64
	header string
	state  string
	trace  string

	span activeSpanInfo

	frozen bool
	buf    *bytes.Buffer
}

func (g *goroutine) addLine(line string) {
	if g.frozen {
		return
	}

	g.buf.WriteString(line)
	g.buf.WriteString("\n")
}

func (g *goroutine) freeze() {
	if g.frozen {
		return
	}

	g.frozen = true
	g.trace = g.buf.String()
	g.buf = nil
}

func newGoroutine(line string) *goroutine {
	idx := strings.Index(line, "[")
	parts := strings.Split(line[idx+1:len(line)-2], ",")
	state := strings.TrimSpace(parts[0])

	if id, err := strconv.Atoi(strings.TrimSpace(line[9:idx])); err == nil {
		return &goroutine{
			id:     uint64(id),
			header: line[0 : idx-1],
			state:  state,
			buf:    &bytes.Buffer{},
		}
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

	g = nil
	scanner := bufio.NewScanner(r)
	startLinePattern := regexp.MustCompile(`^goroutine\s+(\d+)\s+\[(.*)\]:$`)

	for scanner.Scan() {
		line := scanner.Text()
		if startLinePattern.MatchString(line) {
			g = newGoroutine(line)
			if g == nil {
				return nil
			}

			if v, ok := realTimeActiveSpan.Load(g.id); ok {
				g.span = v.(activeSpanInfo)
				dump.add(g)
			}
		} else if line == "" {
			// End of a goroutine section.
			if g != nil {
				g.freeze()
			}
			g = nil
		} else if g != nil {
			g.addLine(line)
		}
	}

	if err := scanner.Err(); err != nil {
		Log("cmd").Errorf("scan goroutine profile: %v", err)
		return nil
	}

	if g != nil {
		g.freeze()
	}

	return dump
}

func curGoroutineID() int64 {
	if goIdOffset > 0 {
		id := goIdFromG()
		if logger.Level >= logrus.DebugLevel {
			dumpId := goIdFromDump()
			if id != dumpId {
				panic(fmt.Sprintf("different goID: getg= %d, dump= %d", id, dumpId))
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

var goIdOffset uintptr

func initGoID() {
	goIdOffset = getOffset()
	if goIdOffset > 0 {
		Log("cmd").Infof("goroutine id from runtime.g (offset = %d)", goIdOffset)
	} else {
		Log("cmd").Infof("goroutine id from stack dump")
	}
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
