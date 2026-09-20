package postgres

import (
	"context"
	"errors"
	"fmt"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/user"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ application.Repository = (*UserRepository)(nil)

type UserRepository struct {
	pool *pgxpool.Pool
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// Register commits the new user and their initial session together.
func (r *UserRepository) Register(ctx context.Context, user domain.User, session application.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := domain.New(user.ID(), user.Login(), user.PasswordHash()); err != nil {
		return err
	}
	if err := validateSession(session); err != nil {
		return err
	}
	if session.UserID != user.ID() {
		return errors.New("initial session must belong to registered user")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin registration: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		"INSERT INTO users (id, login, password_hash) VALUES ($1, $2, $3)",
		string(user.ID()), user.Login(), user.PasswordHash(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_login_key" {
			return domain.ErrLoginTaken
		}
		return fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO user_sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
		session.TokenHash, string(session.UserID), session.ExpiresAt,
	); err != nil {
		return fmt.Errorf("insert registration session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit registration: %w", err)
	}
	return nil
}

func (r *UserRepository) GetByLogin(ctx context.Context, login string) (domain.User, error) {
	var id, storedLogin, passwordHash string
	err := r.pool.QueryRow(ctx,
		"SELECT id, login, password_hash FROM users WHERE login = $1", login,
	).Scan(&id, &storedLogin, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, application.ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get user by login: %w", err)
	}
	user, err := domain.New(domain.ID(id), storedLogin, passwordHash)
	if err != nil {
		return domain.User{}, fmt.Errorf("read stored user: %w", err)
	}
	return user, nil
}

func (r *UserRepository) AddSession(ctx context.Context, session application.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSession(session); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx,
		"INSERT INTO user_sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
		session.TokenHash, string(session.UserID), session.ExpiresAt,
	); err != nil {
		return fmt.Errorf("insert user session: %w", err)
	}
	return nil
}

func (r *UserRepository) GetSession(ctx context.Context, tokenHash string) (application.Session, error) {
	var session application.Session
	var userID string
	err := r.pool.QueryRow(ctx,
		"SELECT user_id, token_hash, expires_at FROM user_sessions WHERE token_hash = $1", tokenHash,
	).Scan(&userID, &session.TokenHash, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Session{}, application.ErrNotFound
	}
	if err != nil {
		return application.Session{}, fmt.Errorf("get user session: %w", err)
	}
	session.UserID = domain.ID(userID)
	return session, nil
}

func validateSession(session application.Session) error {
	if err := domain.ValidateID(session.UserID); err != nil {
		return err
	}
	if session.TokenHash == "" || session.ExpiresAt.IsZero() {
		return errors.New("session token hash and expiration are required")
	}
	return nil
}
