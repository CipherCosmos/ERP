package httperr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// UseJSONFieldNames makes validation errors name the JSON key the client
// actually sent.
//
// By default validator reports the Go struct field, so a missing "family_id"
// came back as "familyID" — a name that appears nowhere in the request and
// that no client can map back to an input. A form could only show "something
// went wrong" instead of marking the empty box.
//
// Call this once, before anything binds. It mutates the validator Gin shares,
// so it belongs at router construction rather than in an init().
func UseJSONFieldNames() {
	v, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		return
	}
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		switch name {
		case "-":
			// Deliberately not on the wire; there is no JSON name to report.
			return ""
		case "":
			return snakeCase(fld.Name)
		default:
			return name
		}
	})
}

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
//	          "fields":{"sku":"is required"}}}
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

	// A domain type whose UnmarshalJSON refuses a value can say why. Without
	// this arm the reason is flattened to "the request could not be processed",
	// which tells a shopkeeper entering a 0.5 g pack size nothing they can act
	// on. The interface is declared here rather than imported, so the domain
	// does not depend on the HTTP layer to be understood by it.
	var describable interface{ ClientMessage() string }
	if errors.As(err, &describable) {
		return BadRequest(describable.ClientMessage()).Wrap(err)
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

// fieldName reports the JSON name of the field that failed.
//
// With UseJSONFieldNames registered, fe.Field() is already the json tag. The
// snakeCase fallback covers a validator that has not been configured — a unit
// test binding directly, say — so the name is still something a client sent
// rather than a Go identifier.
func fieldName(fe validator.FieldError) string {
	name := fe.Field()
	if name == "" {
		name = fe.StructField()
	}
	if strings.ContainsRune(name, '_') || name == strings.ToLower(name) {
		return name
	}
	return snakeCase(name)
}

// snakeCase converts a Go field name to the lower_snake_case form this API
// uses on the wire. Runs of capitals stay together, so HSNCode becomes
// hsn_code rather than h_s_n_code.
func snakeCase(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && !unicode.IsUpper(runes[i-1])
			nextLower := i+1 < len(runes) && !unicode.IsUpper(runes[i+1])
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// reasonFor renders a validator tag as a predicate that reads after the field
// label, so a client can print "Family is required" by joining the two, or show
// the reason alone beside the input it belongs to. Every arm must therefore
// start with a verb; "this field is required" reads as a non-sentence once a
// label is prepended.
func reasonFor(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
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
	case "e164":
		return "must be a phone number in international form"
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
