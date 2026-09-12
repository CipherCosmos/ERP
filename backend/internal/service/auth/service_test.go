package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domain "gbos/internal/domain/user"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/auth"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

const testSecret = "test-signing-secret-at-least-32-characters-long"

func newService(t *testing.T) (*service.Service, *platform.DB, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	db := testsupport.OpenDB(t)

	var storeID uuid.UUID
	if err := db.QueryRowContext(ctx,
		`INSERT INTO stores (name) VALUES ('Test Shop') RETURNING id`).Scan(&storeID); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	svc, err := service.New(db, repo.NewUser(), service.Config{Secret: testSecret})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, db, storeID
}

func mustRegister(t *testing.T, svc *service.Service, storeID uuid.UUID, username, password string, role domain.Role) domain.User {
	t.Helper()
	u, err := svc.Register(context.Background(), username, password, role, storeID)
	if err != nil {
		t.Fatalf("register %s: %v", username, err)
	}
	return u
}

func TestLoginIssuesAWorkingSession(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	mustRegister(t, svc, storeID, "cashier1", "counter-pass-1", domain.RoleCashier)

	token, u, err := svc.Login(ctx, "cashier1", "counter-pass-1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if u.Role != domain.RoleCashier {
		t.Errorf("role = %q, want cashier", u.Role)
	}

	claims, _, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if claims.UserID != u.ID {
		t.Errorf("claims user = %s, want %s", claims.UserID, u.ID)
	}
	if claims.StoreID != storeID {
		t.Errorf("claims store = %s, want %s", claims.StoreID, storeID)
	}
}

func TestUnknownUserAndWrongPasswordAreIndistinguishable(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	mustRegister(t, svc, storeID, "cashier1", "counter-pass-1", domain.RoleCashier)

	_, _, wrongPassword := svc.Login(ctx, "cashier1", "not-the-password")
	_, _, unknownUser := svc.Login(ctx, "nobody-here", "not-the-password")

	// If these differed, the endpoint would be a username oracle.
	if !errors.Is(wrongPassword, domain.ErrBadCredentials) {
		t.Errorf("wrong password gave %v, want ErrBadCredentials", wrongPassword)
	}
	if !errors.Is(unknownUser, domain.ErrBadCredentials) {
		t.Errorf("unknown user gave %v, want ErrBadCredentials", unknownUser)
	}
	if wrongPassword.Error() != unknownUser.Error() {
		t.Errorf("errors differ: %q vs %q", wrongPassword, unknownUser)
	}
}

func TestDeactivationTakesEffectImmediately(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	u := mustRegister(t, svc, storeID, "leaver", "leaving-soon-1", domain.RoleCashier)

	token, _, err := svc.Login(ctx, "leaver", "leaving-soon-1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("token should work before deactivation: %v", err)
	}

	if err := svc.SetActive(ctx, u.ID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	// The previous implementation trusted the token alone, so a dismissed
	// employee kept full access for up to 24 hours.
	if _, _, err := svc.Authenticate(ctx, token); err == nil {
		t.Fatal("a deactivated user's existing token must stop working immediately")
	}
	if _, _, err := svc.Login(ctx, "leaver", "leaving-soon-1"); !errors.Is(err, domain.ErrBadCredentials) {
		t.Errorf("a deactivated user should not be able to sign in, got %v", err)
	}
}

func TestChangingPasswordSignsOutExistingSessions(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	u := mustRegister(t, svc, storeID, "rotator", "first-password-1", domain.RoleManager)

	token, _, err := svc.Login(ctx, "rotator", "first-password-1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// A password change exists to lock somebody out. Leaving their token alive
	// would defeat the point.
	if err := svc.ChangePassword(ctx, u.ID, "first-password-1", "second-password-2"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, token); err == nil {
		t.Error("a token issued before a password change must stop working")
	}

	if _, _, err := svc.Login(ctx, "rotator", "second-password-2"); err != nil {
		t.Errorf("the new password should work: %v", err)
	}
	if _, _, err := svc.Login(ctx, "rotator", "first-password-1"); !errors.Is(err, domain.ErrBadCredentials) {
		t.Errorf("the old password should not work, got %v", err)
	}
}

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	u := mustRegister(t, svc, storeID, "careful", "current-password-1", domain.RoleManager)

	err := svc.ChangePassword(ctx, u.ID, "wrong-current", "new-password-22")
	if !errors.Is(err, domain.ErrBadCredentials) {
		t.Fatalf("expected ErrBadCredentials, got %v", err)
	}
	// The failed attempt must not have changed anything.
	if _, _, err := svc.Login(ctx, "careful", "current-password-1"); err != nil {
		t.Errorf("the original password should still work: %v", err)
	}
}

func TestSignOutEverywhereRevokesEveryToken(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	u := mustRegister(t, svc, storeID, "everywhere", "many-devices-1", domain.RoleManager)

	first, _, err := svc.Login(ctx, "everywhere", "many-devices-1")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	second, _, err := svc.Login(ctx, "everywhere", "many-devices-1")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}

	if err := svc.SignOutEverywhere(ctx, u.ID); err != nil {
		t.Fatalf("sign out: %v", err)
	}
	for name, token := range map[string]string{"first": first, "second": second} {
		if _, _, err := svc.Authenticate(ctx, token); err == nil {
			t.Errorf("the %s session survived signing out everywhere", name)
		}
	}
}

func TestRegisterCannotGrantOwner(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)

	// Otherwise any permission to create accounts becomes a route to owner.
	_, err := svc.Register(context.Background(), "sneaky", "escalate-me-1", domain.RoleOwner, storeID)
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation when granting owner, got %v", err)
	}

	_, err = svc.Register(context.Background(), "invented", "escalate-me-1", domain.Role("superuser"), storeID)
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("expected ErrValidation for an unknown role, got %v", err)
	}
}

func TestPasswordPolicyIsEnforced(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()

	for _, weak := range []string{"", "a", "short1", "alllettersonly", "1234567890"} {
		if _, err := svc.Register(ctx, "weakling", weak, domain.RoleCashier, storeID); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("password %q: expected ErrValidation, got %v", weak, err)
		}
	}
	if _, err := svc.Register(ctx, "sturdy", "a-good-password-1", domain.RoleCashier, storeID); err != nil {
		t.Errorf("a compliant password was rejected: %v", err)
	}
}

func TestTheLastOwnerCannotBeLockedOut(t *testing.T) {
	t.Parallel()
	svc, db, storeID := newService(t)
	ctx := context.Background()

	// Inserted directly, because Register deliberately refuses to grant owner.
	owner := insertOwner(t, db, storeID, "sole-owner")

	if err := svc.SetActive(ctx, owner, false); !errors.Is(err, service.ErrLastOwner) {
		t.Fatalf("expected ErrLastOwner, got %v", err)
	}

	second := insertOwner(t, db, storeID, "second-owner")
	if err := svc.SetActive(ctx, owner, false); err != nil {
		t.Errorf("with a second owner present, deactivation should be allowed: %v", err)
	}
	if err := svc.SetActive(ctx, second, false); !errors.Is(err, service.ErrLastOwner) {
		t.Errorf("the remaining owner should be protected, got %v", err)
	}
}

func TestForgedAndTamperedTokensAreRejected(t *testing.T) {
	t.Parallel()
	svc, _, storeID := newService(t)
	ctx := context.Background()
	mustRegister(t, svc, storeID, "victim", "real-password-1", domain.RoleCashier)

	token, _, err := svc.Login(ctx, "victim", "real-password-1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape: %d parts", len(parts))
	}

	for name, bad := range map[string]string{
		"tampered payload":   parts[0] + "." + tamper(t, parts[1]) + "." + parts[2],
		"stripped signature": parts[0] + "." + parts[1] + ".",
		"alg none":           noneToken(t, parts[1]),
		"garbage":            "not.a.token",
	} {
		if _, _, err := svc.Authenticate(ctx, bad); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// insertOwner creates an owner directly. Register deliberately refuses to
// grant the role, so a test that needs one writes it itself rather than the
// production code growing a back door.
func insertOwner(t *testing.T, db *platform.DB, storeID uuid.UUID, username string) uuid.UUID {
	t.Helper()
	u, err := domain.New(username, "owner-password-1", domain.RoleOwner, storeID)
	if err != nil {
		t.Fatalf("build owner: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO users (id, username, password_hash, role, store_id, is_active)
		 VALUES ($1, $2, $3, 'owner', $4, true)`,
		u.ID, u.Username, u.PasswordHash, storeID); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	return u.ID
}

func tamper(t *testing.T, payload string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	claims["role"] = "owner"
	out, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(out)
}

func noneToken(t *testing.T, payload string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	return header + "." + payload + "."
}
