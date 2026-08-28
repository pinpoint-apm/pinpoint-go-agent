package ppecho

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// The wrapper reports the status echo's HTTPErrorHandler will send, instead of
// invoking that handler itself to read the status off the response.
func Test_statusCode(t *testing.T) {
	if got := statusCode(echo.NewHTTPError(http.StatusNotFound)); got != http.StatusNotFound {
		t.Errorf("statusCode(404) = %d, want 404", got)
	}
	if got := statusCode(errors.New("boom")); got != http.StatusInternalServerError {
		t.Errorf("statusCode(plain error) = %d, want 500", got)
	}
}

// A handler that returns an error must have echo's HTTPErrorHandler run once -
// by echo, from the returned error - not once by the wrapper and again by echo.
func Test_wrapHandler_RunsErrorHandlerOnce(t *testing.T) {
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentId("testAgent"))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := pinpoint.NewTestAgent(config, t)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Shutdown()

	e := echo.New()
	calls := 0
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		calls++
		e.DefaultHTTPErrorHandler(err, c)
	}
	e.GET("/boom", WrapHandler(func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot)
	}))

	req, _ := http.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if calls != 1 {
		t.Errorf("HTTPErrorHandler ran %d times, want 1", calls)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}
