package pphttp

import (
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
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
}

func newDefaultHttpHeaderRecorder(headers []string) *defaultHttpHeaderRecorder {
	return &defaultHttpHeaderRecorder{cfg: headers}
}

func (h *defaultHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, header Header) {
	for _, name := range h.cfg {
		if v := header.Values(name); v != nil && len(v) > 0 {
			vStr := strings.Join(v[:], ",")
			annotation.AppendStringString(int32(key), name, vStr)
		}
	}
}

func (h *defaultHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie Cookie) {
	found := false
	for _, name := range h.cfg {
		cookie.VisitAll(func(cname string, value string) {
			if strings.EqualFold(name, cname) {
				annotation.AppendStringString(pinpoint.AnnotationHttpCookie, cname, value)
				found = true
			}
		})
		if found {
			break
		}
	}
}

type noopHttpHeaderRecorder struct{}

func newNoopHttpHeaderRecorder() *noopHttpHeaderRecorder {
	return &noopHttpHeaderRecorder{}
}

func (h *noopHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, header Header) {
}

func (h *noopHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie Cookie) {
}
