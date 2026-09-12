package handler

import (
	"errors"
	"strconv"
	"strings"

	catalogue "gbos/internal/domain/catalogue"
	"gbos/internal/domain/money"
	"gbos/internal/platform/httperr"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// respond turns a service error into an HTTP response.
//
// Domain validation errors are translated here rather than in httperr, so that
// the platform package stays free of domain imports. Everything else falls
// through to httperr, which maps the database sentinels and renders anything
// unrecognised as a generic 500 with the real error going to the log.
func respond(c *gin.Context, err error) {
	if errors.Is(err, catalogue.ErrValidation) {
		httperr.Respond(c, httperr.Unprocessable(unwrapValidationMessage(err)))
		return
	}
	httperr.Respond(c, err)
}

// unwrapValidationMessage strips the sentinel prefix so the client sees the
// human half of the message and not "validation failed: ".
func unwrapValidationMessage(err error) string {
	msg := err.Error()
	const prefix = "validation failed: "
	if i := strings.Index(msg, prefix); i >= 0 {
		return msg[i+len(prefix):]
	}
	return msg
}

// pathUUID parses a UUID path parameter, responding with 400 when it is
// malformed and reporting whether the handler should continue.
func pathUUID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		httperr.Respond(c, httperr.BadRequest(name+" is not a valid identifier"))
		return uuid.Nil, false
	}
	return id, true
}

// optionalUUID parses a pointer-to-string field that may be absent or blank.
func optionalUUID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// queryPtr returns a pointer to a query value, or nil when it is absent.
func queryPtr(c *gin.Context, name string) *string {
	if v, ok := c.GetQuery(name); ok && v != "" {
		return &v
	}
	return nil
}

// intQuery reads an integer query parameter, falling back on anything
// unparseable rather than rejecting the request.
func intQuery(c *gin.Context, name string, fallback int) int {
	v, ok := c.GetQuery(name)
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func deref(m *money.Money) money.Money {
	if m == nil {
		return money.Zero()
	}
	return *m
}

// boolOr reads an optional boolean field, so that an absent one can mean
// something other than false. A unit that can be both bought and sold is the
// ordinary case, and a plain bool would read a form that omitted the flags as a
// refusal of both.
func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}
