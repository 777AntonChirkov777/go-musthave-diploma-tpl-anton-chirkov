package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	domain "diplom/internal/domain/order"
	"diplom/internal/infrastructure/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
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

func TestMigrateAddsAccrualWithoutChangingExistingOrders(t *testing.T) {
	pool := isolatedDatabase(t)()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Recreate the previously deployed version before inserting an existing order.
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 2); err != nil {
		t.Fatalf("create version 2 schema: %v", err)
	}
	owner := registerOrderOwner(t, pool, "existing-owner")
	want := newOrder(t, "0012345678903", owner, time.Date(2026, 9, 20, 12, 0, 0, 123456000, time.UTC))
	hash := sha256.Sum256([]byte(want.Number()))
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (number_hash, number, user_id, status, uploaded_at)
		VALUES ($1, $2, $3, $4, $5)`,
		hash[:], string(want.Number()), string(want.UserID()), string(want.Status()), want.UploadedAt(),
	); err != nil {
		t.Fatalf("insert existing order: %v", err)
	}

	for range 2 {
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatalf("upgrade order schema: %v", err)
		}
	}
	assertGooseVersions(t, ctx, pool)
	assertOrderCount(t, pool, 1)
	repository := postgres.NewOrderRepository(pool)
	got, err := repository.GetByNumber(ctx, want.Number())
	if err != nil {
		t.Fatal(err)
	}
	assertOrderEqual(t, got, want)
	listed, err := repository.ListByUser(ctx, owner)
	if err != nil || len(listed) != 1 {
		t.Fatalf("migrated orders = %v, error = %v, want one order", listed, err)
	}
	assertOrderEqual(t, listed[0], want)
}

func assertGooseVersions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, version := range []int{1, 2, 3, 4} {
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

func TestMigrateRollsBackAccrualAndWithdrawalsToVersion2(t *testing.T) {
	pool := isolatedDatabase(t)()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertAccrualSchema(t, ctx, pool, true)
	owner := registerOrderOwner(t, pool, "rollback-owner")
	stored := newOrder(t, "12345678903", owner, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	repository := postgres.NewOrderRepository(pool)
	if err := repository.Add(ctx, stored); err != nil {
		t.Fatal(err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 2); err != nil {
		t.Fatalf("roll back to version 2: %v", err)
	}
	assertAccrualSchema(t, ctx, pool, false)
	assertPollingSchema(t, ctx, pool, false)
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM orders WHERE number = $1", string(stored.Number())).Scan(&status); err != nil || status != string(stored.Status()) {
		t.Fatalf("order status after rollback = %q, error = %v, want %q", status, err, stored.Status())
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("re-apply migrations: %v", err)
	}
	assertGooseVersions(t, ctx, pool)
	assertAccrualSchema(t, ctx, pool, true)
	got, err := repository.GetByNumber(ctx, stored.Number())
	if err != nil {
		t.Fatal(err)
	}
	assertOrderEqual(t, got, stored)
}

func assertAccrualSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var withdrawals *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('withdrawals')::text").Scan(&withdrawals); err != nil {
		t.Fatal(err)
	}
	if (withdrawals != nil) != want {
		t.Fatalf("withdrawals table present = %t, want %t", withdrawals != nil, want)
	}
	var dataType, nullable string
	var precision, scale *int
	err := pool.QueryRow(ctx, `
		SELECT data_type, is_nullable, numeric_precision, numeric_scale FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'orders' AND column_name = 'accrual'`,
	).Scan(&dataType, &nullable, &precision, &scale)
	if !want {
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("orders.accrual after rollback: type %q, error %v; want no column", dataType, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("read accrual column: %v", err)
	}
	if dataType != "numeric" || nullable != "YES" || precision == nil || *precision != 20 || scale == nil || *scale != 2 {
		t.Fatalf("orders.accrual = %s nullable %s precision %v scale %v, want nullable numeric(20, 2)", dataType, nullable, precision, scale)
	}
}

func TestMigrateDeclaresBoundedEnumAndExplicitlyNullableColumns(t *testing.T) {
	pool := isolatedDatabase(t)()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	wantLengths := map[string]int{
		"users.id":                 32,
		"users.login":              255,
		"users.password_hash":      256,
		"user_sessions.token_hash": 64,
		"user_sessions.user_id":    32,
		"orders.number":            255,
		"orders.user_id":           32,
		"withdrawals.number":       255,
		"withdrawals.user_id":      32,
	}
	rows, err := pool.Query(ctx, `
		SELECT table_name || '.' || column_name, data_type, character_maximum_length, udt_name, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name IN ('users', 'user_sessions', 'orders', 'withdrawals')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantEnums := map[string]string{"orders.status": "order_status", "orders.check_backoff": "order_check_backoff"}
	var nullableColumns []string
	seenLengths, seenEnums := 0, 0
	for rows.Next() {
		var column, dataType, udtName, nullable string
		var length *int
		if err := rows.Scan(&column, &dataType, &length, &udtName, &nullable); err != nil {
			t.Fatal(err)
		}
		if dataType == "text" {
			t.Errorf("%s is unbounded text", column)
		}
		if want, ok := wantLengths[column]; ok {
			seenLengths++
			if dataType != "character varying" || length == nil || *length != want {
				t.Errorf("%s = %s(%v), want character varying(%d)", column, dataType, length, want)
			}
		}
		if want, ok := wantEnums[column]; ok {
			seenEnums++
			if dataType != "USER-DEFINED" || udtName != want {
				t.Errorf("%s = %s %s, want enum %s", column, dataType, udtName, want)
			}
		}
		if nullable == "YES" {
			nullableColumns = append(nullableColumns, column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seenLengths != len(wantLengths) || seenEnums != len(wantEnums) {
		t.Fatalf("found %d bounded and %d enum columns, want %d and %d", seenLengths, seenEnums, len(wantLengths), len(wantEnums))
	}
	if len(nullableColumns) != 1 || nullableColumns[0] != "orders.accrual" {
		t.Fatalf("nullable columns = %v, want only orders.accrual", nullableColumns)
	}

	var labels string
	if err := pool.QueryRow(ctx, "SELECT array_to_string(enum_range(NULL::order_status), ',')").Scan(&labels); err != nil || labels != "NEW,PROCESSING,INVALID,PROCESSED" {
		t.Fatalf("order_status labels = %q, error = %v", labels, err)
	}
	if err := pool.QueryRow(ctx, "SELECT array_to_string(enum_range(NULL::order_check_backoff), ',')").Scan(&labels); err != nil || labels != "short,long" {
		t.Fatalf("order_check_backoff labels = %q, error = %v", labels, err)
	}

	owner := registerOrderOwner(t, pool, "schema-owner")
	uploadedAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	seedOrder(t, pool, "12345678903", owner, domain.StatusNew, nil, uploadedAt)
	for _, invalid := range []string{"DONE", "new", ""} {
		if _, err := pool.Exec(ctx, "UPDATE orders SET status = $1 WHERE number = '12345678903'", invalid); err == nil {
			t.Fatalf("order status %q was accepted", invalid)
		}
	}
	tooLong := strings.Repeat("0", domain.MaxNumberLength+1-len("12345678903")) + "12345678903"
	hash := sha256.Sum256([]byte(tooLong))
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (number_hash, number, user_id, status, uploaded_at)
		VALUES ($1, $2, $3, 'NEW', $4)`,
		hash[:], tooLong, string(owner), uploadedAt,
	); err == nil {
		t.Fatal("order number longer than the column limit was accepted")
	}
}

func TestMigrateAddsAccrualPollingAndRollsBackToVersion3(t *testing.T) {
	pool := isolatedDatabase(t)()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 3); err != nil {
		t.Fatalf("create version 3 schema: %v", err)
	}
	owner := registerOrderOwner(t, pool, "polling-owner")
	uploadedAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	seedOrder(t, pool, "12345678903", owner, domain.StatusNew, nil, uploadedAt)
	seedOrder(t, pool, "79927398713", owner, domain.StatusProcessed, floatPtr(500.25), uploadedAt)

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("upgrade to polling schema: %v", err)
	}
	assertGooseVersions(t, ctx, pool)
	assertPollingSchema(t, ctx, pool, true)
	var missing, nonZero, notLong int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FILTER (WHERE next_check_at IS NULL), count(*) FILTER (WHERE check_attempts <> 0), count(*) FILTER (WHERE check_backoff IS DISTINCT FROM 'long') FROM orders",
	).Scan(&missing, &nonZero, &notLong); err != nil {
		t.Fatal(err)
	}
	if missing != 0 || nonZero != 0 || notLong != 0 {
		t.Fatalf("migrated orders: %d without next_check_at, %d with nonzero check_attempts, %d without long check_backoff", missing, nonZero, notLong)
	}
	if _, err := pool.Exec(ctx, "UPDATE orders SET check_backoff = 'short' WHERE number = '12345678903'"); err != nil {
		t.Fatalf("set short check_backoff: %v", err)
	}
	for _, invalid := range []string{"medium", "SHORT", ""} {
		if _, err := pool.Exec(ctx, "UPDATE orders SET check_backoff = $1 WHERE number = '12345678903'", invalid); err == nil {
			t.Fatalf("check_backoff %q was accepted", invalid)
		}
	}

	if _, err := provider.DownTo(ctx, 3); err != nil {
		t.Fatalf("roll back to version 3: %v", err)
	}
	assertPollingSchema(t, ctx, pool, false)
	repository := postgres.NewOrderRepository(pool)
	pending, err := repository.GetByNumber(ctx, "12345678903")
	if err != nil || pending.Status() != domain.StatusNew {
		t.Fatalf("pending order after rollback = %+v, %v", pending, err)
	}
	processed, err := repository.GetByNumber(ctx, "79927398713")
	if err != nil {
		t.Fatal(err)
	}
	if accrual, present := processed.Accrual(); processed.Status() != domain.StatusProcessed || !present || accrual != 500.25 {
		t.Fatalf("processed order after rollback = %+v", processed)
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("re-apply migrations: %v", err)
	}
	assertGooseVersions(t, ctx, pool)
	assertPollingSchema(t, ctx, pool, true)
}

func assertPollingSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'orders' AND column_name IN ('next_check_at', 'check_attempts', 'check_backoff')`,
	).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	var index *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('orders_pending_next_check_idx')::text").Scan(&index); err != nil {
		t.Fatal(err)
	}
	var backoffType *string
	if err := pool.QueryRow(ctx, "SELECT to_regtype('order_check_backoff')::text").Scan(&backoffType); err != nil {
		t.Fatal(err)
	}
	wantColumns := 0
	if want {
		wantColumns = 3
	}
	if columns != wantColumns || (index != nil) != want || (backoffType != nil) != want {
		t.Fatalf("polling columns = %d, index present = %t, backoff type present = %t, want %d, %t and %t", columns, index != nil, backoffType != nil, wantColumns, want, want)
	}
}
