package store

import (
	"fmt"
	"testing"
	"time"
)

func Test_readJSON(t *testing.T) {
	var accounts map[string]Account
	err := readJSON("testdata/accounts.node.json", &accounts)
	if err != nil {
		t.Fatalf("something went wrong %v", err)
	}

	for _, value := range accounts {
		fmt.Println(value)
	}
}

func Test_writeJSON(t *testing.T) {
	var accounts map[string]Account
	err := readJSON("testdata/accounts.node.json", &accounts)
	if err != nil {
		t.Fatalf("something went wrong %v", err)
	}

	newAccount := Account{Code: "test_code", CustomServerURL: new("http://localhost:123"), UpdatedAt: time.Now().String(), CreatedAt: time.Now().String()}
	accounts[newAccount.Code] = newAccount
	err = writeJSON("testdata/accounts.test.json", accounts)
	if err != nil {
		t.Fatalf("error while writing json: %v", err)
	}
}
