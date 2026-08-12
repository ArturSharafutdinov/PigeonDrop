package store

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTempFile(dir, name string) (string, error) {
	file, err := os.CreateTemp(dir, name)
	if err != nil {
		return "", err
	}

	defer func() {
		if cErr := file.Close(); cErr != nil && err == nil {
			err = cErr
		}
	}()

	return file.Name(), err
}

func createAccounts(count int) map[string]Account {
	data := map[string]Account{}
	for i := range count {
		currentDate := time.Now().String()
		code := "code_" + strconv.Itoa(i)
		url := new("custom_url_" + strconv.Itoa(i))
		data["code_"+strconv.Itoa(i)] = Account{Code: code, CustomServerURL: url, CreatedAt: currentDate, UpdatedAt: currentDate}
	}

	return data
}

func writeTestData(file string) error {
	data := createAccounts(3)
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return err
	}

	err = os.WriteFile(file, dataBytes, 0o655)
	if err != nil {
		return err
	}

	return nil
}

func createTempStore(dir string) (string, error) {
	path, err := createTempFile(dir, "accounts.json")
	if err != nil {
		return "", err
	}
	return path, nil
}

func newTestStore(t *testing.T, fillData bool) *Accounts {
	t.Helper()
	path, err := createTempStore(t.TempDir())
	if err != nil {
		t.Fatalf("error while creating temp store")
	}

	if fillData {
		err = writeTestData(path)
		if err != nil {
			t.Fatalf("error while filling temp store")
		}
	}

	return &Accounts{path: path}
}

func TestAccounts_loadAll(t *testing.T) {
	s := newTestStore(t, true)

	accounts, err := s.loadAll()
	require.NoError(t, err)

	assert.Equal(t, 3, len(accounts))
	t.Log(accounts)
	for i := range 3 {
		assert.NotEmpty(t, accounts[i].Code)
		assert.NotEmpty(t, *accounts[i].CustomServerURL)
		assert.NotEmpty(t, accounts[i].CreatedAt)
		assert.NotEmpty(t, accounts[i].UpdatedAt)
	}
}

func TestAccounts_writeAll(t *testing.T) {
	s := newTestStore(t, false)

	data := slices.Collect(maps.Values(createAccounts(3)))

	err := s.writeAll(data)
	require.NoError(t, err)

	accounts, err := s.loadAll()
	require.NoError(t, err)

	assert.Equal(t, 3, len(accounts))
	t.Log(accounts)
	for i := range 3 {
		assert.NotEmpty(t, accounts[i].Code)
		assert.NotEmpty(t, *accounts[i].CustomServerURL)
		assert.NotEmpty(t, accounts[i].CreatedAt)
		assert.NotEmpty(t, accounts[i].UpdatedAt)
	}
}

func TestAccounts_Create(t *testing.T) {
	s := newTestStore(t, false)

	got, err := s.Create("abc-123", nil)
	// no error returned
	require.NoError(t, err)

	// got.Code == "abc-123"
	assert.Equal(t, "abc-123", got.Code)
	// got.CustomServerURL == nil
	assert.Nil(t, got.CustomServerURL)
	// got.CreatedAt != empty value
	assert.NotEmpty(t, got.CreatedAt, "CreatedAt must be set on Create")
	// got.CreatedAt != empty value
	assert.NotEmpty(t, got.UpdatedAt, "CreatedAt must be set on Create")
}

func TestAccounts_Get(t *testing.T) {
	s := newTestStore(t, true)

	for i := range 3 {
		code := "code_" + strconv.Itoa(i)
		url := "custom_url_" + strconv.Itoa(i)

		got, err := s.Get(code)
		// no error returned
		require.NoError(t, err)
		t.Log(got)

		// got.Code == "abc-123"
		assert.Equal(t, code, got.Code)
		// got.CustomServerURL != nil
		assert.NotEmpty(t, got.CustomServerURL)
		// got.CustomServerURL == correct value
		assert.Equal(t, url, *got.CustomServerURL)
		// got.CreatedAt != empty value
		assert.NotEmpty(t, got.CreatedAt, "CreatedAt must be set on Create")
		// got.CreatedAt != empty value
		assert.NotEmpty(t, got.UpdatedAt, "CreatedAt must be set on Create")
	}
}

//func TestAccounts_GetOrCreate(t *testing.T) {
//	accountsStore := NewAccounts("testdata/accounts.node.json")
//	_, err := accountsStore.GetOrCreate("m0x1y2z-ab12cd")
//	if err != nil {
//		t.Errorf("something went wrong, %v", err)
//	}
//
//	accountsStore.path = "testdata/accounts.test.json"
//	_, err = accountsStore.GetOrCreate("new-account")
//	if err != nil {
//		t.Errorf("something went wrong, %v", err)
//	}
//}
//
//func TestAccounts_Update(t *testing.T) {
//	accountsStore := NewAccounts("testdata/accounts.node.json")
//	accountsStore.path = "testdata/accounts.test.json"
//	_, err := accountsStore.Update("k9p2q1r-zz88yy", new("I am new url"))
//	if err != nil {
//		t.Errorf("something went wrong, %v", err)
//	}
//}
//
//func TestAccounts_UpdateServerURL(t *testing.T) {
//	accountsStore := NewAccounts("testdata/accounts.node.json")
//	accountsStore.path = "testdata/accounts.test.json"
//	err := accountsStore.UpdateServerURL("k9p2q1r-zz88yy", "I am new url 2")
//	if err != nil {
//		t.Errorf("something went wrong, %v", err)
//	}
//}

// TODO: write correct tests
