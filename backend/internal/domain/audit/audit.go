// Package audit is the trail of who changed what.
//
// Nothing here talks to a database or to HTTP. The redaction rules live here
// rather than in the middleware that happens to call them, because "an audit
// entry never contains a credential" is a property of an entry and is worth
// being able to test without standing up a request.
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value.
var ErrValidation = errors.New("validation failed")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// Entry is one recorded change.
//
// The id is a uuid and is generated as one. A previous call site built it as
// "AUD-" plus a nanosecond clock reading, which the uuid column now rejects
// outright — and which was never unique across processes in the first place.
type Entry struct {
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"user_id"`
	// Username rides along on the read model because the screen showing the
	// trail names people, and a table of raw user ids is not an audit trail
	// anybody can read.
	Username  string    `json:"username"`
	StoreID   uuid.UUID `json:"store_id"`
	Action    string    `json:"action"`
	TableName string    `json:"table_name"`
	RecordID  string    `json:"record_id"`
	OldValue  string    `json:"old_value"`
	NewValue  string    `json:"new_value"`
	CreatedAt time.Time `json:"created_at"`
}

// NewEntryInput is the caller-supplied shape for recording a change.
type NewEntryInput struct {
	UserID    uuid.UUID
	StoreID   uuid.UUID
	Action    string
	TableName string
	RecordID  string
	OldValue  string
	NewValue  string
}

// NewEntry validates and returns an entry ready to persist.
func NewEntry(in NewEntryInput) (Entry, error) {
	action := strings.TrimSpace(in.Action)
	if in.UserID == uuid.Nil {
		return Entry{}, invalid("the user making the change is required")
	}
	if in.StoreID == uuid.Nil {
		return Entry{}, invalid("store is required")
	}
	if action == "" {
		return Entry{}, invalid("action is required")
	}
	return Entry{
		ID:        uuid.New(),
		UserID:    in.UserID,
		StoreID:   in.StoreID,
		Action:    action,
		TableName: strings.TrimSpace(in.TableName),
		RecordID:  strings.TrimSpace(in.RecordID),
		OldValue:  in.OldValue,
		NewValue:  Truncate(in.NewValue),
	}, nil
}

// Filter narrows a trail listing. StoreID is mandatory: one shop's trail is
// not another's to read.
type Filter struct {
	StoreID   uuid.UUID
	UserID    *uuid.UUID
	TableName string
	// Limit is bounded by the repository and reaches SQL as a bound parameter.
	Limit int
}

// ---------------------------------------------------------------------------
// Redaction
// ---------------------------------------------------------------------------

// MaxBodyBytes bounds how much of a request body is read before redaction. A
// caller must not be able to fill the audit table by posting a large document.
const MaxBodyBytes = 1 << 16 // 64 KiB

// MaxStoredBytes bounds what is finally stored, after redaction.
const MaxStoredBytes = 8000

// truncationSuffix marks a body that was cut short, so a reader can tell a
// short body from a clipped one.
const truncationSuffix = "…(truncated)"

// sensitiveKeys never reach the audit log.
//
// Redaction is by key name and not by request path. That is the whole point:
// an exclusion list of paths covers a new endpoint accepting a credential only
// from the day somebody remembers to add it, and the gap between writing the
// endpoint and remembering is exactly when the password is written to a table
// that several roles can read. Matching the key means the coverage arrives
// with the field.
var sensitiveKeys = map[string]bool{
	"password":         true,
	"old_password":     true,
	"new_password":     true,
	"confirm_password": true,
	"password_hash":    true,
	"token":            true,
	"refresh_token":    true,
	"secret":           true,
	"api_key":          true,
	"authorization":    true,
}

// IsSensitive reports whether a JSON key carries a credential.
func IsSensitive(key string) bool { return sensitiveKeys[strings.ToLower(strings.TrimSpace(key))] }

// RedactBody returns a credential-free rendering of a JSON request body.
//
// A body that cannot be parsed is dropped rather than stored unexamined: an
// unparsed body cannot be shown to be free of credentials, and storing it on
// the chance that it is would defeat the rule above.
func RedactBody(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "[unparsed body omitted]"
	}
	out, err := json.Marshal(redactValue(parsed))
	if err != nil {
		return "[unserialisable body omitted]"
	}
	return Truncate(string(out))
}

// redactValue walks a decoded JSON value and replaces every sensitive key,
// however deeply nested — a credential inside an array of objects is still a
// credential.
func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if IsSensitive(k) {
				t[k] = "[redacted]"
				continue
			}
			t[k] = redactValue(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = redactValue(val)
		}
		return t
	default:
		return v
	}
}

// Truncate bounds a stored value at MaxStoredBytes, cutting on a rune boundary
// so the result stays valid UTF-8 and the column stays readable.
func Truncate(s string) string {
	if len(s) <= MaxStoredBytes {
		return s
	}
	cut := MaxStoredBytes
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + truncationSuffix
}

// utf8Start reports whether b begins a UTF-8 encoded rune rather than
// continuing one.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
