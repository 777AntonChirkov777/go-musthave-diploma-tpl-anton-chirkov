package user

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	domain "diplom/internal/domain/user"
)

const sessionLifetime = 24 * time.Hour

var (
	ErrNotFound        = errors.New("user or session not found")
	ErrUnauthenticated = errors.New("invalid credentials or session")
)

type Session struct {
	UserID    domain.ID
	TokenHash string
	ExpiresAt time.Time
}

type Authenticated struct {
	UserID    domain.ID
	Token     string
	ExpiresAt time.Time
}

type Repository interface {
	// Register persists the user and initial session atomically.
	Register(context.Context, domain.User, Session) error
	GetByLogin(context.Context, string) (domain.User, error)
	AddSession(context.Context, Session) error
	GetSession(context.Context, string) (Session, error)
}

type PasswordHasher interface {
	Hash(string) (string, error)
	Compare(hash, password string) (bool, error)
}

type Service struct {
	repository Repository
	passwords  PasswordHasher
	now        func() time.Time
}

func NewService(repository Repository, passwords PasswordHasher, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, passwords: passwords, now: now}
}

func (s *Service) Register(ctx context.Context, login, password string) (Authenticated, error) {
	if err := ctx.Err(); err != nil {
		return Authenticated{}, err
	}
	if err := domain.ValidateCredentials(login, password); err != nil {
		return Authenticated{}, err
	}
	passwordHash, err := s.passwords.Hash(password)
	if err != nil {
		return Authenticated{}, fmt.Errorf("hash password: %w", err)
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return Authenticated{}, fmt.Errorf("generate user ID: %w", err)
	}
	registered, err := domain.New(domain.ID(hex.EncodeToString(idBytes[:])), login, passwordHash)
	if err != nil {
		return Authenticated{}, err
	}
	authenticated, session, err := s.newSession(registered.ID())
	if err != nil {
		return Authenticated{}, err
	}
	if err := s.repository.Register(ctx, registered, session); err != nil {
		return Authenticated{}, fmt.Errorf("register user: %w", err)
	}
	return authenticated, nil
}

func (s *Service) Login(ctx context.Context, login, password string) (Authenticated, error) {
	if err := ctx.Err(); err != nil {
		return Authenticated{}, err
	}
	if err := domain.ValidateCredentials(login, password); err != nil {
		return Authenticated{}, err
	}
	registered, err := s.repository.GetByLogin(ctx, login)
	if errors.Is(err, ErrNotFound) {
		return Authenticated{}, ErrUnauthenticated
	}
	if err != nil {
		return Authenticated{}, fmt.Errorf("get user: %w", err)
	}
	valid, err := s.passwords.Compare(registered.PasswordHash(), password)
	if err != nil {
		return Authenticated{}, fmt.Errorf("compare password: %w", err)
	}
	if !valid {
		return Authenticated{}, ErrUnauthenticated
	}
	authenticated, session, err := s.newSession(registered.ID())
	if err != nil {
		return Authenticated{}, err
	}
	if err := s.repository.AddSession(ctx, session); err != nil {
		return Authenticated{}, fmt.Errorf("add session: %w", err)
	}
	return authenticated, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (domain.ID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Only canonical tokens issued by this service may be used as credentials.
	if len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return "", ErrUnauthenticated
	}
	if decoded, err := base64.RawURLEncoding.Strict().DecodeString(token); err != nil || len(decoded) != 32 {
		return "", ErrUnauthenticated
	}
	session, err := s.repository.GetSession(ctx, hashToken(token))
	if errors.Is(err, ErrNotFound) {
		return "", ErrUnauthenticated
	}
	if err != nil {
		return "", fmt.Errorf("get session: %w", err)
	}
	if !session.ExpiresAt.After(s.now()) || domain.ValidateID(session.UserID) != nil {
		return "", ErrUnauthenticated
	}
	return session.UserID, nil
}

func (s *Service) newSession(userID domain.ID) (Authenticated, Session, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Authenticated{}, Session{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	expiresAt := s.now().Add(sessionLifetime)
	return Authenticated{UserID: userID, Token: token, ExpiresAt: expiresAt},
		Session{UserID: userID, TokenHash: hashToken(token), ExpiresAt: expiresAt}, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
