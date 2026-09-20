package user_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/user"
)

func TestRegisterPreparesUserAndSessionForAtomicPersistence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var capturedUser domain.User
	var capturedSession application.Session
	calls := 0
	repository := stubRepository{register: func(_ context.Context, user domain.User, session application.Session) error {
		calls++
		capturedUser, capturedSession = user, session
		return nil
	}}
	service := application.NewService(repository, testHasher{}, func() time.Time { return now })
	registered, err := service.Register(ctx, "alice", "password")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("atomic Register calls = %d, want 1", calls)
	}
	if registered.UserID == "" || !registered.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected authentication result: %+v", registered)
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(registered.Token)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("session token is not canonical base64url with 32 random bytes: %q, %v", registered.Token, err)
	}
	if capturedUser.ID() != registered.UserID || capturedUser.Login() != "alice" || capturedUser.PasswordHash() != "hash:password" {
		t.Fatalf("user passed to repository = %+v", capturedUser)
	}
	tokenHash := sha256.Sum256([]byte(registered.Token))
	if capturedSession.TokenHash != hex.EncodeToString(tokenHash[:]) || capturedSession.UserID != registered.UserID || capturedSession.ExpiresAt != registered.ExpiresAt {
		t.Fatalf("session passed to repository = %+v", capturedSession)
	}
	if capturedSession.TokenHash == registered.Token {
		t.Fatal("raw session token must not be passed to persistence")
	}
}

func TestRegisterPropagatesDuplicateLogin(t *testing.T) {
	repository := stubRepository{register: func(context.Context, domain.User, application.Session) error {
		return domain.ErrLoginTaken
	}}
	service := application.NewService(repository, testHasher{}, nil)
	got, err := service.Register(context.Background(), "alice", "password")
	if !errors.Is(err, domain.ErrLoginTaken) || got != (application.Authenticated{}) {
		t.Fatalf("Register duplicate = %+v, %v", got, err)
	}
}

func TestLoginIssuesFreshSession(t *testing.T) {
	registered, err := domain.New("user-id", "alice", "hash:password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var capturedSession application.Session
	calls := 0
	repository := stubRepository{
		getUser: func(_ context.Context, login string) (domain.User, error) {
			if login != "alice" {
				t.Fatalf("requested login = %q", login)
			}
			return registered, nil
		},
		addSession: func(_ context.Context, session application.Session) error {
			calls++
			capturedSession = session
			return nil
		},
	}
	service := application.NewService(repository, testHasher{}, func() time.Time { return now })
	previousToken := ""
	for range 2 {
		got, err := service.Login(context.Background(), "alice", "password")
		if err != nil || got.UserID != registered.ID() || !got.ExpiresAt.Equal(now.Add(24*time.Hour)) {
			t.Fatalf("Login = %+v, %v", got, err)
		}
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(got.Token)
		if err != nil || len(decoded) != 32 || got.Token == previousToken {
			t.Fatalf("login must issue a fresh canonical token: %q, %v", got.Token, err)
		}
		tokenHash := sha256.Sum256([]byte(got.Token))
		if capturedSession.UserID != got.UserID || capturedSession.TokenHash != hex.EncodeToString(tokenHash[:]) || capturedSession.ExpiresAt != got.ExpiresAt {
			t.Fatalf("session passed to repository = %+v", capturedSession)
		}
		previousToken = got.Token
	}
	if calls != 2 {
		t.Fatalf("AddSession calls = %d, want 2", calls)
	}
}

func TestLoginRejectsIncorrectCredentials(t *testing.T) {
	registered, err := domain.New("user-id", "alice", "hash:password")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, login, password string
		lookupError           error
	}{
		{name: "wrong password", login: "alice", password: "wrong"},
		{name: "missing login", login: "missing", password: "password", lookupError: application.ErrNotFound},
		{name: "login preserves case", login: "Alice", password: "password", lookupError: application.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := stubRepository{getUser: func(_ context.Context, login string) (domain.User, error) {
				if login != tc.login {
					t.Fatalf("requested login = %q, want %q", login, tc.login)
				}
				return registered, tc.lookupError
			}}
			service := application.NewService(repository, testHasher{}, nil)
			got, err := service.Login(context.Background(), tc.login, tc.password)
			if !errors.Is(err, application.ErrUnauthenticated) || got != (application.Authenticated{}) {
				t.Fatalf("Login = %+v, %v", got, err)
			}
		})
	}
}

func TestAuthenticateChecksStoredSessionAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	token := strings.Repeat("A", 43)
	tokenHash := sha256.Sum256([]byte(token))
	for _, tc := range []struct {
		name        string
		userID      domain.ID
		expiresAt   time.Time
		lookupError error
		valid       bool
	}{
		{name: "valid", userID: "user-id", expiresAt: now.Add(time.Nanosecond), valid: true},
		{name: "expires now", userID: "user-id", expiresAt: now},
		{name: "expired", userID: "user-id", expiresAt: now.Add(-time.Nanosecond)},
		{name: "missing session", lookupError: application.ErrNotFound},
		{name: "invalid user ID", expiresAt: now.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repository := stubRepository{getSession: func(_ context.Context, hash string) (application.Session, error) {
				calls++
				if hash != hex.EncodeToString(tokenHash[:]) {
					t.Fatalf("session lookup hash = %q", hash)
				}
				return application.Session{UserID: tc.userID, TokenHash: hash, ExpiresAt: tc.expiresAt}, tc.lookupError
			}}
			service := application.NewService(repository, nil, func() time.Time { return now })
			got, err := service.Authenticate(context.Background(), token)
			if calls != 1 {
				t.Fatalf("GetSession calls = %d, want 1", calls)
			}
			if tc.valid {
				if err != nil || got != tc.userID {
					t.Fatalf("Authenticate = %q, %v", got, err)
				}
			} else if got != "" || !errors.Is(err, application.ErrUnauthenticated) {
				t.Fatalf("Authenticate = %q, %v", got, err)
			}
		})
	}
}

func TestAuthenticateRejectsMalformedTokensBeforeLookup(t *testing.T) {
	service := application.NewService(nil, nil, nil)
	for _, token := range []string{"", "garbage", strings.Repeat("A", 42), "!" + strings.Repeat("A", 42), strings.Repeat("B", 43)} {
		if got, err := service.Authenticate(context.Background(), token); got != "" || !errors.Is(err, application.ErrUnauthenticated) {
			t.Errorf("Authenticate(%q) = %q, %v", token, got, err)
		}
	}
}

func TestServiceRejectsInvalidInputBeforeAccessingDependencies(t *testing.T) {
	service := application.NewService(nil, nil, nil)
	for _, pair := range [][2]string{{"", "password"}, {" \t", "password"}, {"alice", ""}} {
		if _, err := service.Register(context.Background(), pair[0], pair[1]); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Errorf("Register invalid credentials = %v", err)
		}
		if _, err := service.Login(context.Background(), pair[0], pair[1]); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Errorf("Login invalid credentials = %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Register(ctx, "alice", "password"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Register = %v", err)
	}
	if _, err := service.Login(ctx, "alice", "password"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Login = %v", err)
	}
	if _, err := service.Authenticate(ctx, strings.Repeat("A", 43)); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Authenticate = %v", err)
	}
}

func TestRegistrationDoesNotReturnCredentialsOnFailure(t *testing.T) {
	failure := errors.New("storage unavailable")
	t.Run("password hashing", func(t *testing.T) {
		service := application.NewService(nil, testHasher{err: failure}, nil)
		got, err := service.Register(context.Background(), "alice", "password")
		if !errors.Is(err, failure) || got != (application.Authenticated{}) {
			t.Fatalf("Register = %+v, %v", got, err)
		}
	})
	t.Run("atomic persistence", func(t *testing.T) {
		calls := 0
		repository := stubRepository{register: func(_ context.Context, u domain.User, session application.Session) error {
			calls++
			if session.UserID != u.ID() || session.TokenHash == "" || session.ExpiresAt.IsZero() {
				t.Fatalf("session was not prepared before persisting user: %+v", session)
			}
			return failure
		}}
		service := application.NewService(repository, testHasher{}, nil)
		got, err := service.Register(context.Background(), "alice", "password")
		if !errors.Is(err, failure) || got != (application.Authenticated{}) || calls != 1 {
			t.Fatalf("Register = %+v, %v, repository calls %d", got, err, calls)
		}
	})
}

func TestLoginAndAuthenticationPropagateInternalFailures(t *testing.T) {
	failure := errors.New("internal failure")
	registered, err := domain.New("user-id", "alice", "hash:password")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		repository stubRepository
		hasher     testHasher
	}{
		{name: "lookup", repository: stubRepository{getUser: func(context.Context, string) (domain.User, error) {
			return domain.User{}, failure
		}}},
		{name: "password comparison", repository: stubRepository{getUser: func(context.Context, string) (domain.User, error) {
			return registered, nil
		}}, hasher: testHasher{err: failure}},
		{name: "session persistence", repository: stubRepository{
			getUser:    func(context.Context, string) (domain.User, error) { return registered, nil },
			addSession: func(context.Context, application.Session) error { return failure },
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := application.NewService(tc.repository, tc.hasher, nil)
			got, err := service.Login(context.Background(), "alice", "password")
			if !errors.Is(err, failure) || got != (application.Authenticated{}) {
				t.Fatalf("Login = %+v, %v", got, err)
			}
		})
	}
	service := application.NewService(stubRepository{getSession: func(context.Context, string) (application.Session, error) {
		return application.Session{}, failure
	}}, nil, nil)
	if id, err := service.Authenticate(context.Background(), strings.Repeat("A", 43)); id != "" || !errors.Is(err, failure) {
		t.Fatalf("Authenticate = %q, %v", id, err)
	}
}

type testHasher struct{ err error }

func (h testHasher) Hash(password string) (string, error) { return "hash:" + password, h.err }
func (h testHasher) Compare(hash, password string) (bool, error) {
	return hash == "hash:"+password, h.err
}

type stubRepository struct {
	register   func(context.Context, domain.User, application.Session) error
	getUser    func(context.Context, string) (domain.User, error)
	addSession func(context.Context, application.Session) error
	getSession func(context.Context, string) (application.Session, error)
}

func (r stubRepository) Register(ctx context.Context, u domain.User, session application.Session) error {
	return r.register(ctx, u, session)
}
func (r stubRepository) GetByLogin(ctx context.Context, login string) (domain.User, error) {
	return r.getUser(ctx, login)
}
func (r stubRepository) AddSession(ctx context.Context, session application.Session) error {
	return r.addSession(ctx, session)
}
func (r stubRepository) GetSession(ctx context.Context, hash string) (application.Session, error) {
	return r.getSession(ctx, hash)
}
