// Package auth issues and validates sessions.
//
// The signing key is injected rather than held in a package-level variable, so
// a test can run with its own key and two configurations cannot silently share
// one global.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "gbos/internal/domain/user"
	platform "gbos/internal/platform/postgres"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// ErrLastOwner is returned when deactivating a user would leave the system
// with no active owner, and therefore no way back in.
var ErrLastOwner = errors.New("cannot deactivate the last active owner")

// Repository is the persistence this service needs.
type Repository interface {
	FindByUsername(ctx context.Context, q platform.Querier, username string) (domain.User, error)
	FindByID(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.User, error)
	ListByStore(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]domain.User, error)
	Create(ctx context.Context, q platform.Querier, u domain.User) error
	SetPassword(ctx context.Context, q platform.Querier, id uuid.UUID, hash string) (bool, error)
	SetActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error)
	InvalidateSessions(ctx context.Context, q platform.Querier, id uuid.UUID) (bool, error)
	CountOwners(ctx context.Context, q platform.Querier, excluding uuid.UUID) (int, error)
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Claims are what a session token carries.
type Claims struct {
	UserID  uuid.UUID
	Role    domain.Role
	StoreID uuid.UUID
	IssuedA time.Time
}

type jwtClaims struct {
	Role    string `json:"role"`
	StoreID string `json:"sid"`
	// Epoch is the user's session generation at signing time. It exists because
	// iat is only second-precise and therefore cannot decide whether a token
	// was minted before or after an invalidation in the same second. Absent in
	// tokens issued before this field existed, which reads as zero.
	Epoch int64 `json:"sess,omitempty"`
	jwt.RegisteredClaims
}

// Service signs users in and validates their sessions.
type Service struct {
	db     DB
	repo   Repository
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// Config describes how sessions are signed.
type Config struct {
	// Secret signs tokens. Required.
	Secret string
	// TTL is how long a token is valid. Defaults to 12 hours — a shop shift,
	// rather than the previous 24 hours which outlived the working day.
	TTL time.Duration
}

// New returns an auth service.
func New(db DB, repo Repository, cfg Config) (*Service, error) {
	if cfg.Secret == "" {
		return nil, errors.New("auth: signing secret is required")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Service{db: db, repo: repo, secret: []byte(cfg.Secret), ttl: ttl, now: time.Now}, nil
}

// Login verifies credentials and returns a signed token.
//
// An unknown username, a wrong password and a deactivated account are
// deliberately reported the same way to the caller, so the endpoint cannot be
// used to discover which usernames exist.
func (s *Service) Login(ctx context.Context, username, password string) (token string, u domain.User, err error) {
	u, err = s.repo.FindByUsername(ctx, s.db, username)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			// Still pay the hashing cost, so a missing account cannot be
			// identified by how quickly the request fails.
			_ = domain.User{}.VerifyPassword(password)
			return "", domain.User{}, domain.ErrBadCredentials
		}
		return "", domain.User{}, err
	}
	if err := u.VerifyPassword(password); err != nil {
		return "", domain.User{}, domain.ErrBadCredentials
	}
	if !u.IsActive {
		return "", domain.User{}, domain.ErrBadCredentials
	}

	token, err = s.sign(u)
	if err != nil {
		return "", domain.User{}, err
	}
	return token, u, nil
}

func (s *Service) sign(u domain.User) (string, error) {
	now := s.now()
	claims := jwtClaims{
		Role:    string(u.Role),
		StoreID: u.StoreID.String(),
		Epoch:   u.SessionEpoch(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, &claims).SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// Authenticate validates a token and returns the session it represents.
//
// It re-reads the user on every request. That costs one indexed lookup and buys
// immediate effect for deactivation, role changes and signing out everywhere —
// the previous implementation trusted the token alone, so a deactivated user
// kept working until it expired, up to 24 hours later.
func (s *Service) Authenticate(ctx context.Context, token string) (Claims, domain.User, error) {
	parsed, err := jwt.ParseWithClaims(token, &jwtClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return Claims{}, domain.User{}, fmt.Errorf("invalid session: %w", err)
	}
	claims, ok := parsed.Claims.(*jwtClaims)
	if !ok || !parsed.Valid {
		return Claims{}, domain.User{}, errors.New("invalid session")
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return Claims{}, domain.User{}, errors.New("invalid session subject")
	}
	if claims.IssuedAt == nil {
		return Claims{}, domain.User{}, errors.New("session has no issue time")
	}

	u, err := s.repo.FindByID(ctx, s.db, userID)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return Claims{}, domain.User{}, errors.New("session user no longer exists")
		}
		return Claims{}, domain.User{}, err
	}
	if !u.IsActive {
		return Claims{}, domain.User{}, domain.ErrInactive
	}
	if !u.TokenIsCurrent(claims.Epoch) {
		return Claims{}, domain.User{}, errors.New("session has been signed out")
	}

	return Claims{
		UserID:  u.ID,
		Role:    u.Role,
		StoreID: u.StoreID,
		IssuedA: claims.IssuedAt.Time,
	}, u, nil
}

// Register creates a user. The role must be one an administrator may grant:
// owner is not, so this endpoint cannot be used to escalate.
func (s *Service) Register(ctx context.Context, username, password string, role domain.Role, storeID uuid.UUID) (domain.User, error) {
	if !role.Assignable() {
		return domain.User{}, fmt.Errorf("%w: role %q cannot be granted through this endpoint", domain.ErrValidation, role)
	}
	u, err := domain.New(username, password, role, storeID)
	if err != nil {
		return domain.User{}, err
	}
	if err := s.repo.Create(ctx, s.db, u); err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// ListByStore returns the users attached to a store.
func (s *Service) ListByStore(ctx context.Context, storeID uuid.UUID) ([]domain.User, error) {
	return s.repo.ListByStore(ctx, s.db, storeID)
}

// ChangePassword verifies the current password before setting a new one, and
// signs the user out everywhere.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, oldPassword, newPassword string) error {
	return s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		u, err := s.repo.FindByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		if err := u.VerifyPassword(oldPassword); err != nil {
			return domain.ErrBadCredentials
		}
		hash, err := domain.HashPassword(newPassword)
		if err != nil {
			return err
		}
		found, err := s.repo.SetPassword(ctx, tx, userID, hash)
		if err != nil {
			return err
		}
		if !found {
			return platform.ErrNotFound
		}
		return nil
	})
}

// SetActive deactivates or reactivates a user, refusing to remove the last
// active owner.
func (s *Service) SetActive(ctx context.Context, userID uuid.UUID, active bool) error {
	return s.db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		u, err := s.repo.FindByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		if !active && u.Role == domain.RoleOwner {
			remaining, err := s.repo.CountOwners(ctx, tx, userID)
			if err != nil {
				return err
			}
			if remaining == 0 {
				return ErrLastOwner
			}
		}
		found, err := s.repo.SetActive(ctx, tx, userID, active)
		if err != nil {
			return err
		}
		if !found {
			return platform.ErrNotFound
		}
		return nil
	})
}

// SignOutEverywhere invalidates every session a user currently holds.
func (s *Service) SignOutEverywhere(ctx context.Context, userID uuid.UUID) error {
	found, err := s.repo.InvalidateSessions(ctx, s.db, userID)
	if err != nil {
		return fmt.Errorf("sign out: %w", err)
	}
	if !found {
		return platform.ErrNotFound
	}
	return nil
}
