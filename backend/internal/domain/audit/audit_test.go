package audit_test

import (
	"encoding/json"
	"strings"
	"testing"

	"gbos/internal/domain/audit"
)

// TestRedactionIsByKeyNotByPath is the defect the redaction exists for. Every
// case below is a body that reaches a *different* endpoint; none of them is on
// any exclusion list, and all of them must still come back without the
// credential. Matching on the field name is what makes a newly written
// endpoint covered on the day it is written rather than the day somebody
// remembers it.
func TestRedactionIsByKeyNotByPath(t *testing.T) {
	cases := map[string]string{
		"login":            `{"username":"till1","password":"hunter2"}`,
		"change password":  `{"old_password":"hunter2","new_password":"hunter3"}`,
		"register":         `{"username":"new","password":"hunter2","role":"cashier"}`,
		"nested":           `{"user":{"name":"a","password":"hunter2"}}`,
		"inside an array":  `{"users":[{"name":"a","password":"hunter2"}]}`,
		"mixed case key":   `{"Password":"hunter2"}`,
		"api key":          `{"api_key":"hunter2"}`,
		"bearer token":     `{"token":"hunter2","refresh_token":"hunter2"}`,
		"confirm password": `{"password":"hunter2","confirm_password":"hunter2"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := audit.RedactBody([]byte(body))
			if strings.Contains(got, "hunter2") || strings.Contains(got, "hunter3") {
				t.Errorf("the credential survived redaction: %s", got)
			}
			if !strings.Contains(got, "[redacted]") {
				t.Errorf("the field was dropped rather than redacted: %s", got)
			}
		})
	}
}

func TestRedactionKeepsEverythingElse(t *testing.T) {
	got := audit.RedactBody([]byte(`{"username":"till1","role":"cashier","password":"hunter2"}`))

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("the redacted body is not valid JSON: %s", got)
	}
	if parsed["username"] != "till1" {
		t.Errorf("username = %v, want till1", parsed["username"])
	}
	if parsed["role"] != "cashier" {
		t.Errorf("role = %v, want cashier", parsed["role"])
	}
	if parsed["password"] != "[redacted]" {
		t.Errorf("password = %v, want [redacted]", parsed["password"])
	}
}

// TestAnUnparsableBodyIsNotStored: a body that cannot be parsed cannot be
// shown to be free of credentials, so it is dropped rather than kept on the
// chance that it is clean.
func TestAnUnparsableBodyIsNotStored(t *testing.T) {
	for name, body := range map[string]string{
		"truncated json": `{"password":"hunter2`,
		"not json":       `password=hunter2`,
	} {
		got := audit.RedactBody([]byte(body))
		if strings.Contains(got, "hunter2") {
			t.Errorf("%s: the credential was stored unexamined: %s", name, got)
		}
	}
}

func TestAnEmptyBodyRedactsToNothing(t *testing.T) {
	for _, body := range []string{"", "   ", "\n"} {
		if got := audit.RedactBody([]byte(body)); got != "" {
			t.Errorf("an empty body produced %q", got)
		}
	}
}

func TestTruncateCutsOnARuneBoundary(t *testing.T) {
	// Multi-byte runes straddling the cut must not leave a half-encoded rune
	// behind: the column is text and a broken rune makes the row unreadable.
	long := strings.Repeat("।", audit.MaxStoredBytes)
	got := audit.Truncate(long)
	if len(got) <= audit.MaxStoredBytes/2 {
		t.Fatalf("truncated to %d bytes, which is far shorter than the limit", len(got))
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("truncation split a rune")
		}
	}
	if short := audit.Truncate("small"); short != "small" {
		t.Errorf("a short value was altered: %q", short)
	}
}

func TestIsSensitiveIgnoresCaseAndSpace(t *testing.T) {
	for _, key := range []string{"password", "PASSWORD", " New_Password ", "api_key"} {
		if !audit.IsSensitive(key) {
			t.Errorf("%q was not recognised as sensitive", key)
		}
	}
	for _, key := range []string{"username", "amount", "passwords_hint"} {
		if audit.IsSensitive(key) {
			t.Errorf("%q was wrongly treated as sensitive", key)
		}
	}
}
