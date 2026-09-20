package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"diplom/internal/infrastructure/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateTracksVersionOnceAcrossConcurrentStarts(t *testing.T) {
	openPool := isolatedDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Separate pools model application instances starting against the same schema.
	const instances = 6
	pools := make([]*pgxpool.Pool, instances)
	for i := range pools {
		pools[i] = openPool()
	}
	start := make(chan struct{})
	results := make(chan error, instances)
	var wg sync.WaitGroup
	for _, pool := range pools {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- postgres.Migrate(ctx, pool)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("concurrent migration failed: %v", err)
		}
	}
	if t.Failed() {
		return
	}

	for range 2 {
		if err := postgres.Migrate(ctx, pools[0]); err != nil {
			t.Fatalf("repeat migration: %v", err)
		}
	}
	assertGooseVersions(t, ctx, pools[0])
	var users, sessions, orders int
	if err := pools[0].QueryRow(ctx, "SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM user_sessions), (SELECT count(*) FROM orders)").Scan(&users, &sessions, &orders); err != nil {
		t.Fatalf("read migrated user, session, and order tables: %v", err)
	}
	if users != 0 || sessions != 0 || orders != 0 {
		t.Fatalf("migration created unexpected data: %d users, %d sessions, %d orders", users, sessions, orders)
	}
}

func TestMigratePreservesExistingPreGooseData(t *testing.T) {
	pool := isolatedDatabase(t)()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// This is the schema deployed before goose was introduced. Keep this fixture
	// independent of the current migration so upgrading that schema stays covered.
	const legacySchema = `
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			login TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CONSTRAINT users_login_key UNIQUE (login)
		);
		CREATE TABLE user_sessions (
			token_hash TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX user_sessions_user_id_idx ON user_sessions (user_id);
		CREATE INDEX user_sessions_expires_at_idx ON user_sessions (expires_at);
	`
	if _, err := pool.Exec(ctx, legacySchema); err != nil {
		t.Fatalf("create pre-goose schema: %v", err)
	}
	repository := postgres.NewUserRepository(pool)
	user := newUser(t, "legacy-user", "legacy-login")
	session := newSession(user.ID(), "legacy-token-hash")
	if err := repository.Register(ctx, user, session); err != nil {
		t.Fatalf("populate pre-goose schema: %v", err)
	}
	var userCreated, sessionCreated time.Time
	if err := pool.QueryRow(ctx,
		"SELECT u.created_at, s.created_at FROM users u JOIN user_sessions s ON s.user_id = u.id WHERE u.id = $1",
		string(user.ID()),
	).Scan(&userCreated, &sessionCreated); err != nil {
		t.Fatalf("read original timestamps: %v", err)
	}

	for range 2 {
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatalf("upgrade existing schema: %v", err)
		}
	}
	assertGooseVersions(t, ctx, pool)
	assertOrderCount(t, pool, 0)
	storedUser, err := repository.GetByLogin(ctx, user.Login())
	if err != nil {
		t.Fatalf("read preexisting user after migration: %v", err)
	}
	if storedUser.ID() != user.ID() || storedUser.Login() != user.Login() || storedUser.PasswordHash() != user.PasswordHash() {
		t.Fatalf("migration changed user: got %+v, want %+v", storedUser, user)
	}
	storedSession, err := repository.GetSession(ctx, session.TokenHash)
	if err != nil {
		t.Fatalf("read preexisting session after migration: %v", err)
	}
	if storedSession.UserID != session.UserID || storedSession.TokenHash != session.TokenHash || !storedSession.ExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("migration changed session: got %+v, want %+v", storedSession, session)
	}
	var migratedUserCreated, migratedSessionCreated time.Time
	if err := pool.QueryRow(ctx,
		"SELECT u.created_at, s.created_at FROM users u JOIN user_sessions s ON s.user_id = u.id WHERE u.id = $1",
		string(user.ID()),
	).Scan(&migratedUserCreated, &migratedSessionCreated); err != nil {
		t.Fatalf("read migrated timestamps: %v", err)
	}
	if !migratedUserCreated.Equal(userCreated) || !migratedSessionCreated.Equal(sessionCreated) {
		t.Fatal("migration changed existing account or session creation timestamp")
	}
}

func assertGooseVersions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, version := range []int{1, 2} {
		var records, applied int
		if err := pool.QueryRow(ctx,
			"SELECT count(*), count(*) FILTER (WHERE is_applied) FROM goose_db_version WHERE version_id = $1", version,
		).Scan(&records, &applied); err != nil {
			t.Fatalf("read goose migration history: %v", err)
		}
		if records != 1 || applied != 1 {
			t.Fatalf("goose version %d has %d records and %d applied records, want 1 of each", version, records, applied)
		}
	}
}
