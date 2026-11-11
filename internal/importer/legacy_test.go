package importer

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestLegacyListsPreserveEscapesNullAndEmptyValues(t *testing.T) {
	if value, err := LegacyList(nil); value != nil || err != nil {
		t.Fatal("SQL NULL changed")
	}
	for _, tc := range []struct {
		raw  string
		want []string
	}{{`{}`, []string{}}, {`{"NULL","","a,b","quote\"inside","back\\slash"}`, []string{"NULL", "", "a,b", `quote"inside`, `back\slash`}}} {
		value, err := LegacyList(&tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		var actual []string
		if err = json.Unmarshal([]byte(*value), &actual); err != nil || !reflect.DeepEqual(actual, tc.want) {
			t.Fatalf("roundtrip %q: %#v %v", tc.raw, actual, err)
		}
	}
	for _, raw := range []string{"ordinary old instructions", `{NULL}`, `{{"nested"}}`, `[null]`, `null`} {
		value, err := LegacyList(&raw)
		if err == nil || value == nil || *value != raw {
			t.Fatalf("unsupported value silently changed: %q", raw)
		}
	}
}
