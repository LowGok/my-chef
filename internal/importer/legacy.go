package importer

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// LegacyList decodes a one-dimensional PostgreSQL text-array representation.
// SQL NULL remains nil. Unsupported text stays raw and must be reported.
func LegacyList(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	var values []string
	if strings.HasPrefix(trimmed, "[") {
		var entries []*string
		if json.Unmarshal([]byte(trimmed), &entries) != nil {
			return raw, errors.New("unparsed legacy list")
		}
		for _, v := range entries {
			if v == nil {
				return raw, errors.New("nullable legacy list element")
			}
			values = append(values, *v)
		}
	} else {
		var array pgtype.Array[pgtype.Text]
		if pgtype.NewMap().Scan(pgtype.TextArrayOID, pgtype.TextFormatCode, []byte(*raw), &array) != nil || !array.Valid || len(array.Dims) > 1 || len(array.Dims) == 1 && array.Dims[0].LowerBound != 1 {
			return raw, errors.New("unparsed legacy list")
		}
		for _, v := range array.Elements {
			if !v.Valid {
				return raw, errors.New("nullable legacy list element")
			}
			values = append(values, v.String)
		}
	}
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return raw, err
	}
	v := string(data)
	return &v, nil
}
