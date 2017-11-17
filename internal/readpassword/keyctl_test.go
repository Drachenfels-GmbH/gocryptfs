package readpassword

import (
	"github.com/jsipprell/keyctl"
	"os"
	"testing"

	"github.com/rfjakob/gocryptfs/internal/tlog"
)

func TestMain(m *testing.M) {
	// Shut up info output
	tlog.Info.Enabled = false
	os.Exit(m.Run())
}

func TestKeyctlRequest(t *testing.T) {
	p1 := "ads2q4tw41reg52"
	keyring, err := keyctl.SessionKeyring()
	if err != nil {
		t.Fatal(err)
	}
	_, err = keyring.Add("myKeyID", []byte(p1))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := keyctlRequest("myKeyID")
	if err != nil {
		t.Fatal(err)
	}

	if p1 != p2 {
		t.Fatal("p1=%q != p2=%q", p1, p2)
	}
}

// When keyring returns an empty string, we should crash.
// https://talks.golang.org/2014/testing.slide#23
func TestKeyCtlRequestEmpty(t *testing.T) {
	t.Skip("not implemented")

	/*
		if os.Getenv("TEST_SLAVE") == "1" {
			readPasswordExtpass("echo")
			return
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestExtpassEmpty$")
		cmd.Env = append(os.Environ(), "TEST_SLAVE=1")
		err := cmd.Run()
		if err != nil {
			return
		}
		t.Fatal("empty password should have failed")
	*/
}
