package importer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var expectedColumns = map[string]map[string]string{
	"users":       {"user_id": "int4", "firstname": "text", "lastname": "text", "sex": "varchar", "birthdate": "date", "email": "text", "password": "text", "username": "text", "allergens": "text", "diet": "text", "dislikes": "text", "lastmeal": "date", "cooking_efforts": "text", "cooking_technique": "text", "haspref": "bool"},
	"meals":       {"meal_id": "int8", "creator_id": "int4", "creationdate": "date", "recipetitle": "text", "yield": "text", "preptime": "text", "cooktime": "text", "totaltime": "text", "mychefnotes": "text", "equipment": "text", "instructions": "text", "allergens": "text", "ismeatorfish": "bool", "recipeimg": "text", "cloudinary_public_id": "text", "allergenssafetynote": "text"},
	"ingredients": {"pk_ingredients": "int4", "recipe_id": "int8", "quantity": "numeric", "unit": "text", "ingredient_name": "text", "date": "date"},
}

type snapshot struct {
	conn   *pgx.Conn
	tx     pgx.Tx
	schema string
}

func openSnapshot(ctx context.Context, source, schema string) (*snapshot, error) {
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`).MatchString(schema) {
		return nil, &Error{Code: "unsupported_schema"}
	}
	source = strings.Replace(source, "postgresql+psycopg://", "postgresql://", 1)
	cfg, err := pgx.ParseConfig(source)
	if err != nil {
		return nil, &Error{Code: "source_connection"}
	}
	cfg.ConnectTimeout = 10 * time.Second
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, &Error{Code: "source_connection"}
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		_ = conn.Close(ctx)
		return nil, &Error{Code: "source_snapshot"}
	}
	return &snapshot{conn: conn, tx: tx, schema: schema}, nil
}
func (s *snapshot) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.tx.Rollback(ctx)
	_ = s.conn.Close(ctx)
}
func (s *snapshot) table(name string) string { return pgx.Identifier{s.schema, name}.Sanitize() }
func (s *snapshot) validate(ctx context.Context) error {
	rows, err := s.tx.Query(ctx, "SELECT c.relname,c.relrowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p')", s.schema)
	if err != nil {
		return &Error{Code: "source_schema"}
	}
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		var rls bool
		if err = rows.Scan(&name, &rls); err != nil {
			rows.Close()
			return err
		}
		if expectedColumns[name] == nil || rls {
			rows.Close()
			return &Error{Code: "unsupported_schema", Table: name}
		}
		tables[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(tables) != len(expectedColumns) {
		return &Error{Code: "unsupported_schema"}
	}
	for table, expected := range expectedColumns {
		rows, err = s.tx.Query(ctx, "SELECT column_name,udt_name FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", s.schema, table)
		if err != nil {
			return &Error{Code: "source_schema", Table: table}
		}
		actual := map[string]string{}
		for rows.Next() {
			var name, typ string
			if err = rows.Scan(&name, &typ); err != nil {
				rows.Close()
				return err
			}
			actual[name] = typ
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(actual) != len(expected) {
			return &Error{Code: "unsupported_schema", Table: table}
		}
		for column, typ := range expected {
			if actual[column] != typ {
				return &Error{Code: "unsupported_schema", Table: table, Column: column}
			}
		}
	}
	return nil
}

type Error struct {
	Code   string `json:"code"`
	Table  string `json:"table,omitempty"`
	Column string `json:"column,omitempty"`
	ID     string `json:"id,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("import rejected: %s", e.Code) }
func sourceError(err error, table string) error {
	var known *Error
	if errors.As(err, &known) {
		return err
	}
	return &Error{Code: "source_read", Table: table}
}
