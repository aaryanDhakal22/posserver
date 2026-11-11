package handler

import (
	"strings"
	"testing"
)

func TestDecodeImport(t *testing.T) {
	nd := "{\"phone\":\"4105550101\",\"first_name\":\"Ada\"}\n\n{\"phone\":\"4105550102\",\"addresses\":[{\"line1\":\"1 Test St\",\"zip\":\"21286\"}]}\n"
	recs, err := decodeImport([]byte(nd), true)
	if err != nil || len(recs) != 2 || recs[1].Addresses[0].Zip != "21286" {
		t.Fatalf("ndjson: %+v %v", recs, err)
	}
	if _, err := decodeImport([]byte("{\"phone\":1}\n{oops"), true); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("bad line: %v", err)
	}
	if recs, err := decodeImport([]byte(` [{"phone":"4105550101"}]`), false); err != nil || len(recs) != 1 {
		t.Fatalf("array: %v", err)
	}
	if recs, err := decodeImport([]byte(`{"customers":[{"phone":"4105550101"},{"phone":"4105550102"}]}`), false); err != nil || len(recs) != 2 {
		t.Fatalf("object: %v", err)
	}
	if _, err := decodeImport([]byte(nd), false); err == nil {
		t.Fatal("ndjson without its content type must be refused")
	}
}
