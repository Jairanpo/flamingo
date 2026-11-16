package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID          int32
	Name        string
	DisplayName string
	Role        string
	HasPassword bool
	CreatedAt   time.Time
}

func (u *User) IsAdmin() bool { return u.Role == "ADMIN" }

var (
	ErrBadLogin   = errors.New("wrong user name or password")
	ErrNoSuchUser = errors.New("no such user")
	ErrUserExists = errors.New("that user name is taken")
	ErrBadName    = errors.New("a user name is 2–32 characters: a-z, 0-9, _ . -")
)

var nameRE = regexp.MustCompile(`^[a-z0-9_.-]{2,32}$`)

// NormalizeName lowercases and trims a user name and checks its shape.
func NormalizeName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !nameRE.MatchString(name) {
		return "", ErrBadName
	}
	return name, nil
}

// Store reads and writes users and sessions.
type Store struct {
	Pool       *pgxpool.Pool
	SessionTTL time.Duration
	Now        func() time.Time // for tests; nil means time.Now
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const userCols = `user_id, user_name, user_display_name, usro_code, user_password_hash IS NOT NULL, user_created_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Role, &u.HasPassword, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSuchUser
	}
	return &u, err
}

// CreateUser adds a user. The first user ever is always an admin.
func (s *Store) CreateUser(ctx context.Context, name, display, password string, admin bool) (*User, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(display) == "" {
		display = name
	}
	u, err := scanUser(s.Pool.QueryRow(ctx, `
		INSERT INTO users (user_name, user_display_name, user_password_hash, usro_code)
		VALUES ($1, $2, $3, CASE WHEN $4 OR NOT EXISTS (SELECT 1 FROM users) THEN 'ADMIN' ELSE 'MEMBER' END)
		RETURNING `+userCols, name, strings.TrimSpace(display), hash, admin))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, ErrUserExists
	}
	return u, err
}

func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE user_name = $1`,
		strings.ToLower(strings.TrimSpace(name))))
}

func (s *Store) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetPassword replaces a user's password and signs out all their sessions.
func (s *Store) SetPassword(ctx context.Context, name, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var id int32
		err := tx.QueryRow(ctx, `UPDATE users SET user_password_hash = $2 WHERE user_name = $1 RETURNING user_id`,
			strings.ToLower(strings.TrimSpace(name)), hash).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSuchUser
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
		return err
	})
}

// SetRole makes a user an admin or a member. The last admin can't step down.
func (s *Store) SetRole(ctx context.Context, name string, admin bool) error {
	role := "MEMBER"
	if admin {
		role = "ADMIN"
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Lock the admins so two demotions can't race past the check.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE usro_code = 'ADMIN' FOR UPDATE`); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE users SET usro_code = $2 WHERE user_name = $1
			AND ($2 = 'ADMIN' OR (SELECT count(*) FROM users WHERE usro_code = 'ADMIN' AND user_name <> $1) > 0)`,
			strings.ToLower(strings.TrimSpace(name)), role)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.UserByName(ctx, name); err != nil {
				return err
			}
			return errors.New("that is the last admin; make someone else admin first")
		}
		return nil
	})
}

// DeleteUser removes a user and everything that is theirs alone.
func (s *Store) DeleteUser(ctx context.Context, name string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var role string
		err := tx.QueryRow(ctx, `SELECT usro_code FROM users WHERE user_name = $1 FOR UPDATE`,
			strings.ToLower(strings.TrimSpace(name))).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSuchUser
		}
		if err != nil {
			return err
		}
		if role == "ADMIN" {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE usro_code = 'ADMIN' AND user_name <> $1`,
				strings.ToLower(strings.TrimSpace(name))).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return errors.New("that is the last admin; make someone else admin first")
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM users WHERE user_name = $1`, strings.ToLower(strings.TrimSpace(name)))
		return err
	})
}

// Authenticate checks a name and password. Unknown names and users with no
// password yet fail the same way, and take the same time, as a wrong password.
func (s *Store) Authenticate(ctx context.Context, name, password string) (*User, error) {
	var hash *string
	u, err := scanUserWithHash(s.Pool.QueryRow(ctx, `SELECT `+userCols+`, user_password_hash FROM users WHERE user_name = $1`,
		strings.ToLower(strings.TrimSpace(name))), &hash)
	if err != nil && !errors.Is(err, ErrNoSuchUser) {
		return nil, err
	}
	if u == nil || hash == nil {
		_, _ = CheckPassword(dummyHash, password)
		return nil, ErrBadLogin
	}
	ok, err := CheckPassword(*hash, password)
	if err != nil {
		return nil, fmt.Errorf("user %s: %w", u.Name, err)
	}
	if !ok {
		return nil, ErrBadLogin
	}
	return u, nil
}

func scanUserWithHash(row pgx.Row, hash **string) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Role, &u.HasPassword, &u.CreatedAt, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSuchUser
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// --- sessions ------------------------------------------------------------

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// StartSession signs a user in and returns the token for their cookie.
func (s *Store) StartSession(ctx context.Context, userID int32, userAgent string) (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	expires = now.Add(s.SessionTTL)
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	// Expired sessions are swept here, at the one moment new ones appear.
	if _, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE sess_expires_at < $1`, now); err != nil {
		return "", time.Time{}, err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO sessions (sess_id, user_id, sess_user_agent, sess_created_at, sess_last_seen_at, sess_expires_at)
		VALUES ($1, $2, NULLIF($3, ''), $4, $4, $5)`, tokenHash(token), userID, userAgent, now, expires)
	return token, expires, err
}

// Session returns the signed-in user for a token, or ErrNoSuchUser. A
// session used after half its life is renewed, so an active browser stays
// signed in; renewed reports the new expiry for the cookie.
func (s *Store) Session(ctx context.Context, token string) (u *User, renewed *time.Time, err error) {
	if token == "" {
		return nil, nil, ErrNoSuchUser
	}
	now := s.now()
	var expires time.Time
	var usr User
	err = s.Pool.QueryRow(ctx, `
		SELECT u.user_id, u.user_name, u.user_display_name, u.usro_code, u.user_password_hash IS NOT NULL,
		       u.user_created_at, s.sess_expires_at
		FROM sessions s JOIN users u USING (user_id)
		WHERE s.sess_id = $1 AND s.sess_expires_at > $2`, tokenHash(token), now).
		Scan(&usr.ID, &usr.Name, &usr.DisplayName, &usr.Role, &usr.HasPassword, &usr.CreatedAt, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNoSuchUser
	}
	if err != nil {
		return nil, nil, err
	}
	if expires.Sub(now) < s.SessionTTL/2 {
		next := now.Add(s.SessionTTL)
		if _, err := s.Pool.Exec(ctx, `UPDATE sessions SET sess_last_seen_at = $2, sess_expires_at = $3 WHERE sess_id = $1`,
			tokenHash(token), now, next); err != nil {
			return nil, nil, err
		}
		renewed = &next
	}
	return &usr, renewed, nil
}

func (s *Store) EndSession(ctx context.Context, token string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE sess_id = $1`, tokenHash(token))
	return err
}

// EndAllSessions signs a user out everywhere and reports how many browsers
// that was.
func (s *Store) EndAllSessions(ctx context.Context, name string) (int64, error) {
	if _, err := s.UserByName(ctx, name); err != nil {
		return 0, err
	}
	tag, err := s.Pool.Exec(ctx, `
		DELETE FROM sessions WHERE user_id = (SELECT user_id FROM users WHERE user_name = $1)`,
		strings.ToLower(strings.TrimSpace(name)))
	return tag.RowsAffected(), err
}
