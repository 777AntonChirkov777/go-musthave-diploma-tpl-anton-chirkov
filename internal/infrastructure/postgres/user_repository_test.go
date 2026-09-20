package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/user"
	"diplom/internal/infrastructure/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUserRepositoryPersistsAcrossPoolReopen(t *testing.T) {
	ctx := context.Background()
	openPool := isolatedDatabase(t)
	pool := openPool()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewUserRepository(pool)
	user := newUser(t, "user-1", "alice")
	session := newSession(user.ID(), "first-token-hash")
	if err := repository.Register(ctx, user, session); err != nil {
		t.Fatal(err)
	}
	loginSession := newSession(user.ID(), "second-token-hash")
	if err := repository.AddSession(ctx, loginSession); err != nil {
		t.Fatal(err)
	}
	pool.Close()

	reopenedPool := openPool()
	// Running migrations again must preserve both the account and its sessions.
	if err := postgres.Migrate(ctx, reopenedPool); err != nil {
		t.Fatal(err)
	}
	reopened := postgres.NewUserRepository(reopenedPool)
	stored, err := reopened.GetByLogin(ctx, user.Login())
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID() != user.ID() || stored.Login() != user.Login() || stored.PasswordHash() != user.PasswordHash() {
		t.Fatalf("stored user differs: got %+v, want %+v", stored, user)
	}
	for _, want := range []application.Session{session, loginSession} {
		got, err := reopened.GetSession(ctx, want.TokenHash)
		if err != nil {
			t.Fatal(err)
		}
		if got.UserID != want.UserID || got.TokenHash != want.TokenHash || !got.ExpiresAt.Equal(want.ExpiresAt) {
			t.Fatalf("stored session = %+v, want %+v", got, want)
		}
	}
	if _, err := reopened.GetByLogin(ctx, "absent"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("missing user error = %v, want ErrNotFound", err)
	}
	if _, err := reopened.GetSession(ctx, "absent"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("missing session error = %v, want ErrNotFound", err)
	}
}

func TestUserRepositoryConcurrentRegistrationClaimsLoginOnce(t *testing.T) {
	ctx := context.Background()
	pool := isolatedDatabase(t)()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewUserRepository(pool)
	const attempts = 12
	users := make([]domain.User, attempts)
	for i := range users {
		users[i] = newUser(t, domain.ID(fmt.Sprintf("user-%d", i)), "same-login")
	}
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i, user := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- repository.Register(ctx, user, newSession(user.ID(), fmt.Sprintf("token-hash-%d", i)))
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrLoginTaken):
			conflicts++
		default:
			t.Errorf("unexpected registration error: %v", err)
		}
	}
	if successes != 1 || conflicts != attempts-1 {
		t.Fatalf("got %d successes and %d conflicts, want 1 and %d", successes, conflicts, attempts-1)
	}
	var userCount, sessionCount int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM user_sessions)").Scan(&userCount, &sessionCount); err != nil {
		t.Fatal(err)
	}
	if userCount != 1 || sessionCount != 1 {
		t.Fatalf("stored %d users and %d sessions, want 1 of each", userCount, sessionCount)
	}
}

func TestUserRepositoryFailedSessionRollsBackRegistration(t *testing.T) {
	ctx := context.Background()
	pool := isolatedDatabase(t)()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewUserRepository(pool)
	existing := newUser(t, "existing-id", "existing-login")
	if err := repository.Register(ctx, existing, newSession(existing.ID(), "occupied-token-hash")); err != nil {
		t.Fatal(err)
	}

	newAccount := newUser(t, "new-id", "new-login")
	err := repository.Register(ctx, newAccount, newSession(newAccount.ID(), "occupied-token-hash"))
	if err == nil || errors.Is(err, domain.ErrLoginTaken) {
		t.Fatalf("session collision error = %v, want internal persistence error", err)
	}
	if _, err := repository.GetByLogin(ctx, newAccount.Login()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("failed registration left a user: error = %v", err)
	}
	// A failed registration must leave the login available for a retry.
	if err := repository.Register(ctx, newAccount, newSession(newAccount.ID(), "available-token-hash")); err != nil {
		t.Fatalf("retry registration: %v", err)
	}

	duplicateID := newUser(t, existing.ID(), "different-login")
	err = repository.Register(ctx, duplicateID, newSession(duplicateID.ID(), "another-token-hash"))
	if err == nil || errors.Is(err, domain.ErrLoginTaken) {
		t.Fatalf("ID collision error = %v, want internal persistence error", err)
	}
	if err := repository.AddSession(ctx, newSession("missing-user", "orphan-token-hash")); err == nil {
		t.Fatal("inserting a session for a missing user succeeded")
	}

	invalidAccount := newUser(t, "invalid-id", "invalid-login")
	for _, test := range []struct {
		name    string
		user    domain.User
		session application.Session
	}{
		{"zero user", domain.User{}, newSession(invalidAccount.ID(), "zero-user-hash")},
		{"another user session", invalidAccount, newSession(existing.ID(), "another-user-hash")},
		{"empty user ID", invalidAccount, newSession("", "empty-user-hash")},
		{"empty token hash", invalidAccount, newSession(invalidAccount.ID(), "")},
		{"zero expiration", invalidAccount, application.Session{UserID: invalidAccount.ID(), TokenHash: "zero-expiration-hash"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := repository.Register(ctx, test.user, test.session); err == nil {
				t.Fatal("registration with invalid user or session succeeded")
			}
			if _, err := repository.GetByLogin(ctx, invalidAccount.Login()); !errors.Is(err, application.ErrNotFound) {
				t.Fatalf("invalid registration left a user: error = %v", err)
			}
			if _, err := repository.GetSession(ctx, test.session.TokenHash); !errors.Is(err, application.ErrNotFound) {
				t.Fatalf("invalid registration left a session: error = %v", err)
			}
		})
	}
	for _, session := range []application.Session{
		newSession("", "empty-user-hash"),
		newSession(existing.ID(), ""),
		{UserID: existing.ID(), TokenHash: "zero-expiration-hash"},
	} {
		if err := repository.AddSession(ctx, session); err == nil {
			t.Fatalf("inserting invalid session succeeded: %+v", session)
		}
		if _, err := repository.GetSession(ctx, session.TokenHash); !errors.Is(err, application.ErrNotFound) {
			t.Fatalf("invalid session was stored: error = %v", err)
		}
	}
}

func newUser(t *testing.T, id domain.ID, login string) domain.User {
	t.Helper()
	user, err := domain.New(id, login, "stored-password-hash")
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func newSession(userID domain.ID, tokenHash string) application.Session {
	return application.Session{
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond),
	}
}

// Each test owns a randomly named schema. TEST_DATABASE_URI should point to a
// PostgreSQL database whose role can create schemas; existing tables are untouched.
func isolatedDatabase(t *testing.T) func() *pgxpool.Pool {
	t.Helper()
	uri := os.Getenv("TEST_DATABASE_URI")
	if uri == "" {
		t.Skip("set TEST_DATABASE_URI to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if err := admin.Ping(ctx); err != nil {
		t.Fatalf("connect to TEST_DATABASE_URI: %v", err)
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schemaName := "auth_test_" + hex.EncodeToString(suffix[:])
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(uri)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	return func() *pgxpool.Pool {
		t.Helper()
		pool, err := pgxpool.NewWithConfig(context.Background(), cfg.Copy())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
}
