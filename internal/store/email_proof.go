package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/LowGok/my-chef/migrations"
)

type SignUpJob struct {
	ID        string `gorm:"primaryKey"`
	Requested int64
	Expires   int64
	Draft     string
}

func (SignUpJob) TableName() string { return "signup_requests" }

type SignUpProof struct {
	ID         string `gorm:"primaryKey"`
	Hash       string
	AddressTag string
	Expires    int64
	Consumed   *int64
	Draft      string
}

func (SignUpProof) TableName() string { return "signup_proofs" }

type EmailVerificationProof struct {
	ID         string `gorm:"primaryKey"`
	UserID     int64
	Hash       string
	AddressTag string
	Expires    int64
	Consumed   *int64
}

func (EmailVerificationProof) TableName() string { return "email_verification_proofs" }

type PendingEmailChange struct {
	ID                 string `gorm:"primaryKey"`
	UserID             int64
	OriginalEmail      string
	OriginalKey        string
	OriginalVerifiedAt *int64
	ProposedEmail      string
	ProposedKey        string
	Expires            int64
	CurrentApproved    bool
	ProposedApproved   bool
}

func (PendingEmailChange) TableName() string { return "pending_email_changes" }

type EmailChangeProof struct {
	Hash     string `gorm:"primaryKey"`
	ChangeID string
	Purpose  string
	Consumed *int64
}

func (EmailChangeProof) TableName() string { return "email_change_proofs" }

func (s *Store) migrateSignUp(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 6 || checksum != emailLimitsSchemaChecksum() || dirty {
		return errors.New("unexpected schema before sign-up migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.SignUp); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=7,checksum=?,dirty=0 WHERE version=6", signupSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateEmailVerification(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 7 || checksum != signupSchemaChecksum() || dirty {
		return errors.New("unexpected schema before email verification migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.EmailVerification); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=8,checksum=?,dirty=0 WHERE version=7", emailVerificationSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateEmailChange(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 8 || checksum != emailVerificationSchemaChecksum() || dirty {
		return errors.New("unexpected schema before email change migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.EmailChange); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=9,checksum=?,dirty=0 WHERE version=8", emailChangeSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
