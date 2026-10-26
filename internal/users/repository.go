package users

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"drivethrough/internal/models"
	"drivethrough/pkg/apperr"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateUser(ctx context.Context, name, phone string, role models.UserRole) (*models.User, error) {
	var user models.User
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (name, phone, role, is_active)
		VALUES ($1, $2, $3, TRUE)
		RETURNING id, name, phone, email, role, is_active, created_at, updated_at
	`, name, phone, role.String()).Scan(
		&user.ID, &user.Name, &user.Phone, &user.Email, &user.Role, &user.IsActive, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Repository) GetByPhone(ctx context.Context, phone string) (*models.User, error) {
	var user models.User
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, phone, email, role, is_active, created_at, updated_at
		FROM users WHERE phone = $1
	`, phone).Scan(
		&user.ID, &user.Name, &user.Phone, &user.Email, &user.Role, &user.IsActive, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("user not found")
		}
		return nil, err
	}
	return &user, nil
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	var user models.User
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, phone, email, role, is_active, created_at, updated_at
		FROM users WHERE id = $1
	`, id).Scan(
		&user.ID, &user.Name, &user.Phone, &user.Email, &user.Role, &user.IsActive, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("user not found")
		}
		return nil, err
	}
	return &user, nil
}

func (r *Repository) UpdateName(ctx context.Context, id uuid.UUID, name string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE users SET name = $1, updated_at = NOW() WHERE id = $2`, name, id)
	return err
}

func (r *Repository) PromoteToDriver(ctx context.Context, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE users SET role = 'DRIVER', updated_at = NOW() WHERE id = $1`, userID)
	return err
}

func (r *Repository) ListDrivers(ctx context.Context, limit, offset int) ([]models.User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, phone, email, role, is_active, created_at, updated_at
		FROM users WHERE role = 'DRIVER'
		ORDER BY created_at DESC LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Name, &u.Phone, &u.Email, &u.Role, &u.IsActive, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
