package asm

import (
	"runtime"
	"sync"
	"testing"
)

// Getg is the goroutine id's whole foundation: goroutine.go adds the goid
// offset to it and reads an int64. It has to return the current g - non-nil,
// the same value for as long as the goroutine runs, and a different one on
// another goroutine - on every architecture that has an assembly body.
func TestGetg(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	g := Getg()
	if g == nil {
		t.Fatal("Getg returned nil")
	}
	if again := Getg(); again != g {
		t.Fatalf("Getg is not stable on one goroutine: %p then %p", g, again)
	}

	var other uintptr
	var wg sync.WaitGroup
	wg.Go(func() { other = uintptr(Getg()) })
	wg.Wait()
	if other == 0 {
		t.Fatal("Getg returned nil on another goroutine")
	}
	if other == uintptr(g) {
		t.Fatalf("two goroutines share one g: %#x", other)
	}
}
