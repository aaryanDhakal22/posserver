package authSvc

import "testing"

func TestVerifyPassword(t *testing.T) {
	bc, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	legacy := HashKey("s3cret")

	cases := []struct {
		name               string
		pw, stored         string
		wantOK, wantRehash bool
	}{
		{"bcrypt match", "s3cret", bc, true, false},
		{"bcrypt mismatch", "nope", bc, false, false},
		{"legacy sha256 match → rehash", "s3cret", legacy, true, true},
		{"legacy sha256 mismatch", "nope", legacy, false, false},
		{"empty stored", "s3cret", "", false, false},
		{"empty password", "", bc, false, false},
	}
	for _, tc := range cases {
		ok, rehash := VerifyPassword(tc.pw, tc.stored)
		if ok != tc.wantOK || rehash != tc.wantRehash {
			t.Errorf("%s: got (%v,%v) want (%v,%v)", tc.name, ok, rehash, tc.wantOK, tc.wantRehash)
		}
	}
	if bc == legacy || bc[:2] != "$2" {
		t.Fatalf("HashPassword should produce bcrypt, got %q", bc)
	}
}
