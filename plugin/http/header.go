package http

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"net/http"
	"strings"
)

type httpHeaderRecorder interface {
	recordHeader(annotation pinpoint.Annotation, aKey int, header http.Header)
	recordCookie(annotation pinpoint.Annotation, cookie []*http.Cookie)
}

type allHttpHeaderRecorder struct{}

func newAllHttpHeaderRecorder() *allHttpHeaderRecorder {
	return &allHttpHeaderRecorder{}
}

func (h *allHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, header http.Header) {
	for name, values := range header {
		vStr := strings.Join(values[:], ",")
		annotation.AppendStringString(int32(key), name, vStr)
	}
}

func (h *allHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
	for _, c := range cookie {
		annotation.AppendStringString(pinpoint.AnnotationHttpCookie, c.Name, c.Value)
	}
}

type defaultHttpHeaderRecorder struct {
	cfg []string
}

func newDefaultHttpHeaderRecorder(headers []string) *defaultHttpHeaderRecorder {
	return &defaultHttpHeaderRecorder{cfg: headers}
}

func (h *defaultHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, header http.Header) {
	for _, name := range h.cfg {
		if v := header.Values(name); v != nil && len(v) > 0 {
			vStr := strings.Join(v[:], ",")
			annotation.AppendStringString(int32(key), name, vStr)
		}
	}
}

func (h *defaultHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
	for _, name := range h.cfg {
		for _, c := range cookie {
			if strings.EqualFold(name, c.Name) {
				annotation.AppendStringString(pinpoint.AnnotationHttpCookie, c.Name, c.Value)
				break
			}
		}
	}
}

type noopHttpHeaderRecorder struct{}

func newNoopHttpHeaderRecorder() *noopHttpHeaderRecorder {
	return &noopHttpHeaderRecorder{}
}

func (h *noopHttpHeaderRecorder) recordHeader(annotation pinpoint.Annotation, key int, header http.Header) {
}

func (h *noopHttpHeaderRecorder) recordCookie(annotation pinpoint.Annotation, cookie []*http.Cookie) {
}
