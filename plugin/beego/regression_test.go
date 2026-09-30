package ppbeego

import (
	"net/http"
	"net/http/httptest"
	"testing"

	beegoContext "github.com/beego/beego/v2/server/web/context"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// Output.Body - behind ServeJSON, Render and the rest - writes the header and
// resets Output.Status to 0, and a plain write never sets it, so the status
// has to be read from the writer beego itself logs.
func TestServerFilterChain_RecordsTheStatusWrittenThroughOutputBody(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantStatus int
		wantFail   bool
	}{
		{name: "a body without a status is a 200", wantStatus: http.StatusOK},
		{name: "a success status", status: http.StatusCreated, wantStatus: http.StatusCreated},
		{name: "a server error fails the span", status: http.StatusInternalServerError, wantStatus: http.StatusInternalServerError, wantFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startAgent(t)

			rec := httptest.NewRecorder()
			ctx := newBeegoContext(httptest.NewRequest(http.MethodGet, "/hello", nil), rec)
			var tracer pinpoint.Tracer
			ServerFilterChain()(func(c *beegoContext.Context) {
				tracer = pinpoint.TracerFromRequestContext(c.Request)
				if tt.status != 0 {
					c.Output.SetStatus(tt.status)
				}
				require.NoError(t, c.Output.Body([]byte("body")))
			})(ctx)

			assert.Equal(t, tt.wantStatus, rec.Code, "wire status")
			assert.Equal(t, tt.wantStatus, statusAnnotation(t, tracer), "recorded status")
			assert.Equal(t, tt.wantFail, spanOf(t, tracer)["Err"] != float64(0))
		})
	}
}
