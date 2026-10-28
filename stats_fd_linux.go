//go:build linux

package pinpoint

import (
	"encoding/binary"
	"syscall"
)

// countOpenFDs counts this process's open file descriptors by walking
// /proc/self/fd with getdents directly, so that no name is ever materialized:
// the entries are only counted. It reports false when the directory cannot be
// read, and the caller falls back to gopsutil.
//
// The buffer is a stack array, so the walk allocates nothing however many
// descriptors are open (syscall.Open copies the path once); the kernel fills
// it with linux_dirent64 records (d_ino 8, d_off 8, d_reclen 2, d_type 1,
// then the NUL-terminated name).
func countOpenFDs() (int32, bool) {
	fd, err := syscall.Open("/proc/self/fd", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, false
	}
	defer syscall.Close(fd)

	var buf [8 << 10]byte
	n := 0
	for {
		r, err := syscall.ReadDirent(fd, buf[:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, false
		}
		if r <= 0 {
			break
		}
		for off := 0; off+19 <= r; {
			reclen := int(binary.NativeEndian.Uint16(buf[off+16:]))
			if reclen < 19 || off+reclen > r {
				return 0, false // a record the kernel would never write
			}
			name := buf[off+19 : off+reclen]
			// Skip "." and "..": every other entry is a descriptor number.
			if !(len(name) > 2 && name[0] == '.' && (name[1] == 0 || (name[1] == '.' && name[2] == 0))) {
				n++
			}
			off += reclen
		}
	}
	// The directory handle this count was taken through is one of the entries.
	return int32(n - 1), true
}
