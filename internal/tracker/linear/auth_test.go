package linear

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

func sample() *saved {
	return &saved{ClientID: "c1", TokenURL: "https://auth.example/token", Token: &oauth2.Token{AccessToken: "a1", RefreshToken: "r1"}}
}

func TestStoreRoundTripKeychain(t *testing.T) {
	keyring.MockInit()
	st := Store{File: filepath.Join(t.TempDir(), "token.json")}
	if err := st.save(sample()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.File); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file written although keychain succeeded")
	}
	got, err := st.load()
	if err != nil || got.ClientID != "c1" || got.Token.RefreshToken != "r1" {
		t.Fatalf("got %+v err=%v", got, err)
	}
	if err := st.Delete(); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.load(); got != nil {
		t.Fatal("credential survived Delete")
	}
}

func TestStoreFallsBackToPrivateFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keychain"))
	st := Store{File: filepath.Join(t.TempDir(), "sub", "token.json")}
	if err := st.save(sample()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(st.File)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v err=%v", fi, err)
	}
	got, err := st.load()
	if err != nil || got.Token.AccessToken != "a1" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestStoreEmpty(t *testing.T) {
	keyring.MockInit()
	got, err := Store{File: filepath.Join(t.TempDir(), "none.json")}.load()
	if got != nil || err != nil {
		t.Fatalf("got %+v err=%v", got, err)
	}
}
