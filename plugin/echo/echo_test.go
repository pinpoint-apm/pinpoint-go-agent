package ppecho

import (
	"github.com/labstack/echo"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
)

func handler1(c echo.Context) error { return c.String(http.StatusOK, "hello-get") }
func handler2(c echo.Context) error { return c.String(http.StatusOK, "hello-post") }

func Test_makeHandlerNameMap(t *testing.T) {
	t.Run("new handlerNameMap", func(t *testing.T) {
		e := echo.New()

		e.GET("/hello", handler1)
		e.POST("/hello", handler2)

		req := httptest.NewRequest(http.MethodPost, "/hello", nil)
		rec := httptest.NewRecorder()

		c := e.NewContext(req, rec)
		makeHandlerNameMap(c)

		assert.Equal(t, 2, len(handlerNameMap))
		assert.Equal(t, "github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo.handler1()", handlerNameMap[key{"GET", "/hello"}])
		assert.Equal(t, "github.com/pinpoint-apm/pinpoint-go-agent/plugin/echo.handler2()", handlerNameMap[key{"POST", "/hello"}])
	})
}
