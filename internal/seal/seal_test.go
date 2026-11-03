package seal

import (
	"bytes"
	"errors"
	"testing"
)

// fast keeps the tests quick; the format carries the parameters, so Open
// reads them from the file as it would DefaultParams.
var fast = Params{Time: 1, Memory: 64, Threads: 1}

func TestSealAndOpen(t *testing.T) {
	k, err := newKey("correct horse", fast)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("SQLite format 3\x00 the configuration")
	sealed, err := k.Seal(content)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("configuration")) {
		t.Fatal("the content shows through")
	}
	if !Sealed(sealed) {
		t.Fatal("not recognized as sealed")
	}
	got, err := Open("correct horse", sealed)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("Open = %q, %v", got, err)
	}

	again, _ := k.Seal(content)
	if bytes.Equal(again, sealed) {
		t.Fatal("two seals of the same content are the same: the nonce did not change")
	}

	if _, err := Open("wrong horse", sealed); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("a wrong password: %v", err)
	}
	for _, i := range []int{len(magic) + 2, headerSize - 1, len(sealed) - 1} {
		changed := bytes.Clone(sealed)
		changed[i] ^= 1
		if _, err := Open("correct horse", changed); err == nil {
			t.Fatalf("a file changed at byte %d opened", i)
		}
	}
	if _, err := Open("correct horse", []byte("SQLite format 3\x00")); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("a plain database: %v", err)
	}
	newer := bytes.Clone(sealed)
	newer[len(magic)] = 2
	if _, err := Open("correct horse", newer); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a newer version: %v", err)
	}
}

func TestKeyEncoding(t *testing.T) {
	k, err := newKey("pw", fast)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeKey(k.Encode())
	if err != nil || !back.Equal(k) {
		t.Fatalf("round trip: %v", err)
	}
	sealed, _ := back.Seal([]byte("x"))
	if got, err := Open("pw", sealed); err != nil || string(got) != "x" {
		t.Fatalf("a decoded key seals for the password: %q, %v", got, err)
	}
	if _, err := DecodeKey("nope"); err == nil {
		t.Fatal("decoded garbage")
	}
}

func TestDefaultParamsOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("derives a 64 MiB key")
	}
	k, err := NewKey("a long password")
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := k.Seal([]byte("x"))
	if _, err := Open("a long password", sealed); err != nil {
		t.Fatal(err)
	}
}
