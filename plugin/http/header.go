package pphttp

import (
	"net/textproto"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

type Header interface {
	Values(key string) []string
	VisitAll(f func(name string, values []string))
}

type Cookie interface {
	VisitAll(f func(name string, value string))
}

type httpHeaderRecorder interface {
	recordHeader(annotation pinpoint.Annotation, aKey int, header Header)
	recordCookie(annotation pinpoint.Annotation, cookie Cookie)
}

type allHttpHeaderRecorder struct{}

func newAllHttpHeaderRecorder() *allHttpHeaderRecorder {
	return &allHttpHeaderRecorder{}
}

func (h *allHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, header Header) {
	header.VisitAll(func(name string, values []string) {
		vStr := strings.Join(values[:], ",")
		annotation.AppendStringString(int32(key), name, vStr)
	})
}

func (h *allHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie Cookie) {
	cookie.VisitAll(func(name string, value string) {
		annotation.AppendStringString(pinpoint.AnnotationHttpCookie, name, value)
	})
}

type defaultHttpHeaderRecorder struct {
	cfg []string
	// lookup is cfg in canonical form, computed once, for the net/http
	// adapter (lookupKey). The annotation keeps the configured spelling.
	lookup []string
}

// lookupKey is the spelling to look a header up by in hdr: canonical, computed
// once, for the net/http adapter, whose Values canonicalizes the key it is
// given on every call - allocating for a name that is not canonical already -
// and name itself for any other. fasthttp with its header name normalizing
// disabled matches only the spelling on the wire, which a canonical key
// missed for every name that is not canonical.
func lookupKey(hdr Header, name, canonical string) string {
	if _, ok := hdr.(header); ok {
		return canonical
	}
	return name
}

func newDefaultHttpHeaderRecorder(headers []string) *defaultHttpHeaderRecorder {
	lookup := make([]string, len(headers))
	for i, name := range headers {
		lookup[i] = textproto.CanonicalMIMEHeaderKey(name)
	}
	return &defaultHttpHeaderRecorder{cfg: headers, lookup: lookup}
}

func (h *defaultHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, hdr Header) {
	for i, name := range h.cfg {
		if v := hdr.Values(lookupKey(hdr, name, h.lookup[i])); len(v) > 0 {
			vStr := strings.Join(v[:], ",")
			annotation.AppendStringString(int32(key), name, vStr)
		}
	}
}

func (h *defaultHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie Cookie) {
	// One pass over the cookies: VisitAll may parse the Cookie header lazily,
	// so visiting once per configured name would re-parse it per name. This
	// also records every configured cookie, where the old per-name loop
	// stopped at the first configured name that matched anything.
	cookie.VisitAll(func(cname string, value string) {
		for _, name := range h.cfg {
			if strings.EqualFold(name, cname) {
				annotation.AppendStringString(pinpoint.AnnotationHttpCookie, cname, value)
				break
			}
		}
	})
}

type noopHttpHeaderRecorder struct{}

func newNoopHttpHeaderRecorder() *noopHttpHeaderRecorder {
	return &noopHttpHeaderRecorder{}
}

func (h *noopHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, header Header) {
}

func (h *noopHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie Cookie) {
}
