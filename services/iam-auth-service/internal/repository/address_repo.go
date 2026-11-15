package repository

import (
	"context"
	"errors"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AddressRepository stores address books in iam.customer_addresses. Writes lock the owner's user row first,
// so concurrent changes to one address book run one after another and the one-default rule always holds.
type AddressRepository struct {
	pool *pgxpool.Pool
}

func NewAddressRepository(pool *pgxpool.Pool) *AddressRepository {
	return &AddressRepository{pool: pool}
}

const addressColumns = `id, user_id, recipient, phone, line1, ward, district, province, is_default, created_at, updated_at`

func scanAddress(row pgx.Row) (Address, error) {
	var a Address
	err := row.Scan(&a.ID, &a.UserID, &a.Recipient, &a.Phone, &a.Line1, &a.Ward, &a.District, &a.Province, &a.IsDefault, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func (r *AddressRepository) List(ctx context.Context, userID string) ([]Address, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+addressColumns+` FROM iam.customer_addresses WHERE user_id = $1
		ORDER BY is_default DESC, created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	out := []Address{}
	for rows.Next() {
		a, err := scanAddress(rows)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		out = append(out, a)
	}
	return out, dependency.Wrap(rows.Err())
}

func (r *AddressRepository) Get(ctx context.Context, userID, id string) (Address, error) {
	a, err := scanAddress(r.pool.QueryRow(ctx, `SELECT `+addressColumns+` FROM iam.customer_addresses WHERE user_id = $1 AND id = $2`, userID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Address{}, ErrAddressNotFound
	}
	return a, dependency.Wrap(err)
}

// inBook runs fn in a transaction that holds the lock of the user's address book.
func (r *AddressRepository) inBook(ctx context.Context, userID string, fn func(tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM iam.users WHERE id = $1 FOR UPDATE`, userID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		return dependency.Wrap(err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return dependency.Wrap(tx.Commit(ctx))
}

func (r *AddressRepository) Create(ctx context.Context, userID string, in AddressInput) (Address, error) {
	id, err := newAddressID()
	if err != nil {
		return Address{}, err
	}
	var created Address
	err = r.inBook(ctx, userID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM iam.customer_addresses WHERE user_id = $1`, userID).Scan(&count); err != nil {
			return dependency.Wrap(err)
		}
		if count >= MaxAddresses {
			return ErrAddressLimit
		}
		makeDefault := in.IsDefault || count == 0
		if makeDefault {
			if _, err := tx.Exec(ctx, `UPDATE iam.customer_addresses SET is_default = FALSE WHERE user_id = $1 AND is_default`, userID); err != nil {
				return dependency.Wrap(err)
			}
		}
		a, err := scanAddress(tx.QueryRow(ctx, `INSERT INTO iam.customer_addresses
			(id, user_id, recipient, phone, line1, ward, district, province, is_default)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+addressColumns,
			id, userID, in.Recipient, in.Phone, in.Line1, in.Ward, in.District, in.Province, makeDefault))
		created = a
		return dependency.Wrap(err)
	})
	return created, err
}

func (r *AddressRepository) Update(ctx context.Context, userID, id string, in AddressInput) (Address, error) {
	var updated Address
	err := r.inBook(ctx, userID, func(tx pgx.Tx) error {
		var wasDefault bool
		err := tx.QueryRow(ctx, `SELECT is_default FROM iam.customer_addresses WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, id).Scan(&wasDefault)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAddressNotFound
		}
		if err != nil {
			return dependency.Wrap(err)
		}
		makeDefault := in.IsDefault || wasDefault
		if makeDefault && !wasDefault {
			if _, err := tx.Exec(ctx, `UPDATE iam.customer_addresses SET is_default = FALSE WHERE user_id = $1 AND is_default`, userID); err != nil {
				return dependency.Wrap(err)
			}
		}
		updated, err = scanAddress(tx.QueryRow(ctx, `UPDATE iam.customer_addresses SET recipient = $3, phone = $4, line1 = $5,
			ward = $6, district = $7, province = $8, is_default = $9, updated_at = NOW()
			WHERE user_id = $1 AND id = $2 RETURNING `+addressColumns,
			userID, id, in.Recipient, in.Phone, in.Line1, in.Ward, in.District, in.Province, makeDefault))
		return dependency.Wrap(err)
	})
	return updated, err
}

func (r *AddressRepository) Delete(ctx context.Context, userID, id string) error {
	return r.inBook(ctx, userID, func(tx pgx.Tx) error {
		var wasDefault bool
		err := tx.QueryRow(ctx, `DELETE FROM iam.customer_addresses WHERE user_id = $1 AND id = $2 RETURNING is_default`, userID, id).Scan(&wasDefault)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAddressNotFound
		}
		if err != nil {
			return dependency.Wrap(err)
		}
		if !wasDefault {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE iam.customer_addresses SET is_default = TRUE WHERE id = (
			SELECT id FROM iam.customer_addresses WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1)`, userID)
		return dependency.Wrap(err)
	})
}

func newAddressID() (string, error) {
	id, err := newUserID() // "usr-" + 24 random hex characters
	if err != nil {
		return "", err
	}
	return "addr-" + id[len("usr-"):], nil
}
