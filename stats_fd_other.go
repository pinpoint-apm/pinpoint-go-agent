//go:build !linux

package pinpoint

// countOpenFDs has no cheap source outside Linux; numFD asks gopsutil.
func countOpenFDs() (int32, bool) {
	return 0, false
}
