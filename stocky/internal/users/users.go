// Package users holds the small amount of user logic Stocky needs.
package users

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound = errors.New("unknown user")
	ErrExists   = errors.New("user already exists")
	ErrInvalid  = errors.New("id must be 1-64 letters, digits, _ or -, and name must not be empty")
)

// idPattern keeps ids URL-safe, since they appear in paths like /stats/:userId.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Exists reports whether a user with this id exists.
func Exists(ctx context.Context, q Querier, id string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&ok)
	return ok, err
}

// MustExist returns ErrNotFound when the user does not exist.
func MustExist(ctx context.Context, q Querier, id string) error {
	ok, err := Exists(ctx, q, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// Create inserts a user. It returns ErrInvalid for a bad id or empty name and
// ErrExists when the id is taken.
func Create(ctx context.Context, q Querier, id, name string) error {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	if !idPattern.MatchString(id) || name == "" {
		return ErrInvalid
	}
	tag, err := q.Exec(ctx, `INSERT INTO users (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`, id, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrExists
	}
	return nil
}
