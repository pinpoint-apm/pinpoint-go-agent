package ppechov4

import (
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/pinpoint-apm/pinpoint-go-agent"
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
		assert.Equal(t, "github.com/pinpoint-apm/pinpoint-go-agent/plugin/echov4.handler1()", handlerNameMap[key{"GET", "/hello"}])
		assert.Equal(t, "github.com/pinpoint-apm/pinpoint-go-agent/plugin/echov4.handler2()", handlerNameMap[key{"POST", "/hello"}])
	})
}

func Test_Middleware(t *testing.T) {
	t.Run("middleware", func(t *testing.T) {
		opts := []pinpoint.ConfigOption{
			pinpoint.WithAppName("GoEchov4Test"),
		}
		cfg, _ := pinpoint.NewConfig(opts...)
		agent, _ := pinpoint.NewTestAgent(cfg, t)
		defer agent.Shutdown()

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
}
