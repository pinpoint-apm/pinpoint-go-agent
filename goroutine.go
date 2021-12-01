package pinpoint

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type MetaType int

var (
	MetaState    MetaType = 0
	MetaDuration MetaType = 1

	durationPattern = regexp.MustCompile(`^\d+ minutes$`)
)

// Goroutine contains a goroutine info.
type Goroutine struct {
	id       int
	header   string
	trace    string
	lines    int
	duration int // In minutes.
	metas    map[MetaType]string

	lineMd5    []string
	fullMd5    string
	fullHasher hash.Hash
	duplicates []int

	frozen bool
	buf    *bytes.Buffer
}

// AddLine appends a line to the goroutine info.
func (g *Goroutine) AddLine(l string) {
	if !g.frozen {
		g.lines++
		g.buf.WriteString(l)
		g.buf.WriteString("\n")

		if strings.HasPrefix(l, "\t") {
			parts := strings.Split(l, " ")
			if len(parts) != 2 {
				fmt.Println("ignored one line for digest")
				return
			}

			fl := strings.TrimSpace(parts[0])

			h := md5.New()
			io.WriteString(h, fl)
			g.lineMd5 = append(g.lineMd5, string(h.Sum(nil)))

			io.WriteString(g.fullHasher, fl)
		}
	}
}

// Freeze freezes the goroutine info.
func (g *Goroutine) Freeze() {
	if !g.frozen {
		g.frozen = true
		g.trace = g.buf.String()
		g.buf = nil

		g.fullMd5 = string(g.fullHasher.Sum(nil))
	}
}

// Print outputs the goroutine details to w.
func (g Goroutine) Print(w io.Writer) error {
	if _, err := fmt.Fprint(w, g.header); err != nil {
		return err
	}
	if len(g.duplicates) > 0 {
		if _, err := fmt.Fprintf(w, " %d times: [[", len(g.duplicates)); err != nil {
			return err
		}
		for i, id := range g.duplicates {
			if i > 0 {
				if _, err := fmt.Fprint(w, ", "); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprint(w, id); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(w, "]"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, g.trace); err != nil {
		return err
	}
	return nil
}

// NewGoroutine creates and returns a new Goroutine.
func NewGoroutine(metaline string) (*Goroutine, error) {
	idx := strings.Index(metaline, "[")
	parts := strings.Split(metaline[idx+1:len(metaline)-2], ",")
	metas := map[MetaType]string{
		MetaState: strings.TrimSpace(parts[0]),
	}

	duration := 0
	if len(parts) > 1 {
		value := strings.TrimSpace(parts[1])
		metas[MetaDuration] = value
		if durationPattern.MatchString(value) {
			if d, err := strconv.Atoi(value[:len(value)-8]); err == nil {
				duration = d
			}
		}
	}

	idstr := strings.TrimSpace(metaline[9:idx])
	id, err := strconv.Atoi(idstr)
	if err != nil {
		return nil, err
	}

	return &Goroutine{
		id:         id,
		lines:      1,
		header:     metaline[0 : idx-1],
		buf:        &bytes.Buffer{},
		duration:   duration,
		metas:      metas,
		fullHasher: md5.New(),
		duplicates: []int{},
	}, nil
}

// GoroutineDump defines a goroutine dump.
type GoroutineDump struct {
	goroutines []*Goroutine
}

// Add appends a goroutine info to the list.
func (gd *GoroutineDump) Add(g *Goroutine) {
	gd.goroutines = append(gd.goroutines, g)
}

// Dedup finds goroutines with duplicated stack traces and keeps only one copy
// of them.
func (gd *GoroutineDump) Dedup() {
	m := map[string][]int{}
	for _, g := range gd.goroutines {
		if _, ok := m[g.fullMd5]; ok {
			m[g.fullMd5] = append(m[g.fullMd5], g.id)
		} else {
			m[g.fullMd5] = []int{g.id}
		}
	}

	kept := make([]*Goroutine, 0, len(gd.goroutines))

outter:
	for digest, ids := range m {
		for _, g := range gd.goroutines {
			if g.fullMd5 == digest {
				g.duplicates = ids
				kept = append(kept, g)
				continue outter
			}
		}
	}

	if len(gd.goroutines) != len(kept) {
		fmt.Printf("Dedupped %d, kept %d\n", len(gd.goroutines), len(kept))
		gd.goroutines = kept
	}
}

// Diff shows the difference between two dumps.
func (gd *GoroutineDump) Diff(another *GoroutineDump) (*GoroutineDump, *GoroutineDump, *GoroutineDump) {
	lonly := map[int]*Goroutine{}
	ronly := map[int]*Goroutine{}
	common := map[int]*Goroutine{}

	for _, v := range gd.goroutines {
		lonly[v.id] = v
	}
	for _, v := range another.goroutines {
		if _, ok := lonly[v.id]; ok {
			delete(lonly, v.id)
			common[v.id] = v
		} else {
			ronly[v.id] = v
		}
	}
	return NewGoroutineDumpFromMap(lonly), NewGoroutineDumpFromMap(common), NewGoroutineDumpFromMap(ronly)
}

// Save saves the goroutine dump to the given file.
func (gd GoroutineDump) Save(fn string) error {
	f, err := os.Create(fn)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, g := range gd.goroutines {
		if err := g.Print(f); err != nil {
			return err
		}
	}
	return nil
}

// Sort sorts the goroutine entries.
func (gd *GoroutineDump) Sort() {
	fmt.Printf("# of goroutines: %d\n", len(gd.goroutines))
}

// Summary prints the summary of the goroutine dump.
func (gd GoroutineDump) Summary() {
	fmt.Printf("# of goroutines: %d\n", len(gd.goroutines))
	stats := map[string]int{}
	if len(gd.goroutines) > 0 {
		for _, g := range gd.goroutines {
			stats[g.metas[MetaState]]++
		}
		fmt.Println()
	}
	if len(stats) > 0 {
		states := make([]string, 0, 10)
		for k := range stats {
			states = append(states, k)
		}
		sort.Sort(sort.StringSlice(states))

		for _, k := range states {
			fmt.Printf("%15s: %d\n", k, stats[k])
		}
		fmt.Println()
	}
}

func (gd *GoroutineDump) Search(s string) *Goroutine {
	for _, g := range gd.goroutines {
		if g.header == s {
			return g
		}
	}

	return nil
}

// NewGoroutineDump creates and returns a new GoroutineDump.
func NewGoroutineDump() *GoroutineDump {
	return &GoroutineDump{
		goroutines: []*Goroutine{},
	}
}

// NewGoroutineDumpFromMap creates and returns a new GoroutineDump from a map.
func NewGoroutineDumpFromMap(gs map[int]*Goroutine) *GoroutineDump {
	gd := &GoroutineDump{
		goroutines: []*Goroutine{},
	}
	for _, v := range gs {
		gd.goroutines = append(gd.goroutines, v)
	}
	return gd
}

var (
	startLinePattern = regexp.MustCompile(`^goroutine\s+(\d+)\s+\[(.*)\]:$`)
)

func loadProfile(fn string) (*GoroutineDump, error) {
	fn = strings.Trim(fn, "\"")
	f, err := os.Open(fn)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dump := NewGoroutineDump()
	var goroutine *Goroutine

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if startLinePattern.MatchString(line) {
			goroutine, err = NewGoroutine(line)
			if err != nil {
				return nil, err
			}
			dump.Add(goroutine)
		} else if line == "" {
			// End of a goroutine section.
			if goroutine != nil {
				goroutine.Freeze()
			}
			goroutine = nil
		} else if goroutine != nil {
			goroutine.AddLine(line)
		}
	}

	if goroutine != nil {
		goroutine.Freeze()
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return dump, nil
}
