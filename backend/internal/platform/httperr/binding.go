package httperr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-playground/validator/v10"
)

// FromBinding converts the error c.ShouldBindJSON returns into a 400 with
// per-field reasons, so handlers stop returning the raw binding message.
//
// Gin's binding message for a failed `binding:"required"` tag reads
//
//	Key: 'CreateProductRequest.SKU' Error:Field validation for 'SKU' failed on the 'required' tag
//
// which names Go types to an end user. This produces
//
//	{"error":{"code":"validation_failed","message":"The request contains invalid fields.",
//	          "fields":{"sku":"this field is required"}}}
//
// It handles the three shapes Gin actually emits: validator.ValidationErrors,
// json.UnmarshalTypeError (wrong JSON type for a field), and an empty body.
func FromBinding(err error) *Error {
	if err == nil {
		return nil
	}

	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) {
		fields := make(map[string]string, len(validationErrs))
		for _, fe := range validationErrs {
			fields[fieldName(fe)] = reasonFor(fe)
		}
		return Validation("The request contains invalid fields.", fields).Wrap(err)
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		name := typeErr.Field
		if name == "" {
			name = "body"
		}
		return Validation("The request contains invalid fields.", map[string]string{
			name: fmt.Sprintf("expected %s", friendlyType(typeErr.Type.String())),
		}).Wrap(err)
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return BadRequest("The request body is not valid JSON.").Wrap(err)
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return BadRequest("The request body is empty.").Wrap(err)
	}

	return BadRequest("The request could not be processed.").Wrap(err)
}

// fieldName prefers the JSON name from the struct tag, falling back to the Go
// field name lowercased. Clients send JSON; they should be told about JSON.
func fieldName(fe validator.FieldError) string {
	if name := fe.Field(); name != "" {
		return strings.ToLower(name[:1]) + name[1:]
	}
	return fe.StructField()
}

// reasonFor renders a validator tag as a sentence an end user can act on.
func reasonFor(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "this field is required"
	case "email":
		return "must be a valid email address"
	case "min":
		return fmt.Sprintf("must be at least %s", fe.Param())
	case "max":
		return fmt.Sprintf("must be at most %s", fe.Param())
	case "len":
		return fmt.Sprintf("must be exactly %s characters", fe.Param())
	case "gt":
		return fmt.Sprintf("must be greater than %s", fe.Param())
	case "gte":
		return fmt.Sprintf("must be %s or more", fe.Param())
	case "lt":
		return fmt.Sprintf("must be less than %s", fe.Param())
	case "lte":
		return fmt.Sprintf("must be %s or less", fe.Param())
	case "oneof":
		return "must be one of: " + strings.ReplaceAll(fe.Param(), " ", ", ")
	case "uuid", "uuid4":
		return "must be a valid identifier"
	case "numeric":
		return "must be a number"
	case "alphanum":
		return "must contain only letters and numbers"
	default:
		return "is not valid"
	}
}

// friendlyType renders a Go type name in terms a client would recognise.
func friendlyType(goType string) string {
	switch {
	case strings.HasPrefix(goType, "int"), strings.HasPrefix(goType, "uint"),
		strings.HasPrefix(goType, "float"):
		return "a number"
	case goType == "bool":
		return "true or false"
	case goType == "string":
		return "a string"
	case strings.HasPrefix(goType, "[]"):
		return "an array"
	default:
		return "a different type"
	}
}
