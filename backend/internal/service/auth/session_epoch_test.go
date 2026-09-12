package auth_test

import (
	"context"
	"testing"

	domain "gbos/internal/domain/user"
)

// TestSigningInRightAfterAPasswordChangeWorks pins a bug that made the most
// security-sensitive action in the system look broken half the time.
//
// Signing out everywhere used to be enforced by comparing the token's iat
// against the invalidation instant. A JWT carries iat in whole seconds, so a
// token minted 900ms after the invalidation claimed to predate it and was
// rejected. The user changed their password, signed in, got a token, and was
// bounced straight back to the login screen by the next 401 — until the second
// ticked over. Which half of the second they landed in decided whether it
// worked, so it read as a flaky login rather than a defect.
//
// The loop is what makes this a real test: one iteration could land in the
// lucky half of a second and pass against the broken code.
func TestSigningInRightAfterAPasswordChangeWorks(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()

	u := mustRegister(t, svc, storeID, "rotator", "first-password-1", domain.RoleManager)
	password := "first-password-1"

	for i := 0; i < 12; i++ {
		next := "rotated-password-" + string(rune('a'+i))
		if err := svc.ChangePassword(ctx, u.ID, password, next); err != nil {
			t.Fatalf("round %d: change password: %v", i, err)
		}
		password = next

		token, _, err := svc.Login(ctx, "rotator", password)
		if err != nil {
			t.Fatalf("round %d: login after change: %v", i, err)
		}
		// No sleep: the whole point is that the new session works immediately.
		if _, _, err := svc.Authenticate(ctx, token); err != nil {
			t.Fatalf("round %d: the session issued right after a password change was refused: %v", i, err)
		}
	}
}

// TestAnOlderSessionInTheSameSecondIsStillRevoked is the other half of the
// rule. Loosening the comparison to "at or after" would have fixed the test
// above by letting a token minted microseconds *before* the invalidation
// survive, which is the hole the strict comparison existed to close.
func TestAnOlderSessionInTheSameSecondIsStillRevoked(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()

	u := mustRegister(t, svc, storeID, "revoked", "first-password-1", domain.RoleCashier)

	for i := 0; i < 12; i++ {
		old, _, err := svc.Login(ctx, "revoked", "first-password-1")
		if err != nil {
			t.Fatalf("round %d: login: %v", i, err)
		}
		if _, _, err := svc.Authenticate(ctx, old); err != nil {
			t.Fatalf("round %d: the session should work before it is revoked: %v", i, err)
		}
		// Sign out everywhere, in the same second the token was minted.
		if err := svc.SignOutEverywhere(ctx, u.ID); err != nil {
			t.Fatalf("round %d: sign out everywhere: %v", i, err)
		}
		if _, _, err := svc.Authenticate(ctx, old); err == nil {
			t.Fatalf("round %d: a token minted in the same second as the sign-out was still honoured", i)
		}
	}
}
