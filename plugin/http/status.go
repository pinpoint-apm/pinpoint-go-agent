package pphttp

import (
	"strconv"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type httpStatusCode interface {
	isError(code int) bool
}

type httpStatusInformational struct{}

func newHttpStatusInformational() *httpStatusInformational {
	return &httpStatusInformational{}
}

func (h *httpStatusInformational) isError(code int) bool {
	return 100 <= code && code <= 199
}

type httpStatusSuccess struct{}

func newHttpStatusSuccess() *httpStatusSuccess {
	return &httpStatusSuccess{}
}

func (h *httpStatusSuccess) isError(code int) bool {
	return 200 <= code && code <= 299
}

type httpStatusRedirection struct{}

func newHttpStatusRedirection() *httpStatusRedirection {
	return &httpStatusRedirection{}
}

func (h *httpStatusRedirection) isError(code int) bool {
	return 300 <= code && code <= 399
}

type httpStatusClientError struct{}

func newHttpStatusClientError() *httpStatusClientError {
	return &httpStatusClientError{}
}

func (h *httpStatusClientError) isError(code int) bool {
	return 400 <= code && code <= 499
}

type httpStatusServerError struct{}

func newHttpStatusServerError() *httpStatusServerError {
	return &httpStatusServerError{}
}

func (h *httpStatusServerError) isError(code int) bool {
	return 500 <= code && code <= 599
}

type httpStatusDefault struct {
	statusCode int
}

func newHttpStatusDefault(code int) *httpStatusDefault {
	return &httpStatusDefault{
		statusCode: code,
	}
}

func (h *httpStatusDefault) isError(code int) bool {
	return h.statusCode == code
}

type httpStatusError struct {
	errors []httpStatusCode
}

func newHttpStatusError() *httpStatusError {
	return &httpStatusError{
		errors: setupHttpStatusErrors(),
	}
}

func setupHttpStatusErrors() []httpStatusCode {
	var errors []httpStatusCode

	cfgErrors := pinpoint.GetConfig().StringSlice(CfgHttpServerStatusCodeErrors)
	trimStringSlice(cfgErrors)

	for _, s := range cfgErrors {
		if strings.EqualFold(s, "5xx") {
			errors = append(errors, newHttpStatusServerError())
		} else if strings.EqualFold(s, "4xx") {
			errors = append(errors, newHttpStatusClientError())
		} else if strings.EqualFold(s, "3xx") {
			errors = append(errors, newHttpStatusRedirection())
		} else if strings.EqualFold(s, "2xx") {
			errors = append(errors, newHttpStatusSuccess())
		} else if strings.EqualFold(s, "1xx") {
			errors = append(errors, newHttpStatusInformational())
		} else {
			c, e := strconv.Atoi(s)
			if e != nil {
				c = -1
			}
			errors = append(errors, newHttpStatusDefault(c))
		}
	}

	return errors
}

func (h *httpStatusError) isError(code int) bool {
	for _, h := range h.errors {
		if h.isError(code) {
			return true
		}
	}
	return false
}
