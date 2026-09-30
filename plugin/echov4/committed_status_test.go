package ppechov4

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
)

// statusAnnotation reads the recorded HTTP status back out of the span JSON:
// {"key":46,"value":{"Field":{"IntValue":500}}}.
func statusAnnotation(t *testing.T, tracer pinpoint.Tracer) int {
	t.Helper()
	annotations, _ := spanOf(t, tracer)["Annotations"].([]interface{})
	for _, a := range annotations {
		m, _ := a.(map[string]interface{})
		if key, _ := m["key"].(float64); int(key) != pinpoint.AnnotationHttpStatusCode {
			continue
		}
		value, _ := m["value"].(map[string]interface{})
		field, _ := value["Field"].(map[string]interface{})
		n, _ := field["IntValue"].(float64)
		return int(n)
	}
	return 0
}

// A handler that wrote its response and then returned an error: echo's error
// handler leaves a committed response alone, so the wire keeps the status the
// handler wrote and the span must record that one, not the error's.
func TestMiddleware_RecordsTheCommittedStatusOverTheErrors(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	e := echo.New()
	e.Use(Middleware())
	e.GET("/", func(c echo.Context) error {
		tracer = pinpoint.TracerFromRequestContext(c.Request())
		if err := c.String(http.StatusOK, "ok"); err != nil {
			return err
		}
		return errors.New("late failure")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, http.StatusOK, statusAnnotation(t, tracer), "the recorded status must be the one on the wire")
}
