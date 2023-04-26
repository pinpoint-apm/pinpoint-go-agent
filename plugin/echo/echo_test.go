package ppecho

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
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

func Test_Middleware(t *testing.T) {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoEchovTest"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, _ := pinpoint.NewTestAgent(cfg, t)
	defer agent.Shutdown()

	t.Run("middleware", func(t *testing.T) {
		e := echo.New()
		e.Use(Middleware())

		var tracer pinpoint.Tracer
		e.GET("/hello", func(c echo.Context) error {
			tracer = pinpoint.TracerFromRequestContext(c.Request())
			assert.Equal(t, true, tracer.IsSampled())
			return c.String(http.StatusBadGateway, "hello-get")
		})

		r := httptest.NewRequest(http.MethodGet, "/hello", nil)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)

		j := tracer.JsonString()
		fmt.Println(string(j))

		var m map[string]interface{}
		json.Unmarshal(j, &m)
		assert.Equal(t, "/hello", m["RpcName"])
		assert.Equal(t, "example.com", m["EndPoint"])
		assert.Equal(t, "192.0.2.1", m["RemoteAddr"])
		assert.Equal(t, float64(1), m["Err"])

		a := m["Annotations"].([]interface{})[0].(map[string]interface{})
		assert.Equal(t, float64(pinpoint.AnnotationHttpStatusCode), a["key"])
	})

	t.Run("check if the middleware propagates an error", func(t *testing.T) {
		var capturedError error

		e := echo.New()
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(echoCtx echo.Context) error {
				resErr := next(echoCtx)
				capturedError = resErr
				return resErr
			}
		})
		e.Use(Middleware())
		e.GET("/hello", func(c echo.Context) error {
			err := fmt.Errorf("error from handler")
			c.Error(err)
			return err
		})

		req := httptest.NewRequest(http.MethodGet, "/hello", nil)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Error(t, capturedError)
		assert.Equal(t, "error from handler", capturedError.Error())
	})
}
