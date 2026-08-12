package store

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"
)

var ErrNotFound = errors.New("account not found")
var ErrAlreadyExists = errors.New("account already exists")

type Account struct {
	Code            string  `json:"code"`
	CustomServerURL *string `json:"customServerUrl"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
}

func (a Account) String() string {
	customServerURL := "null"
	if a.CustomServerURL != nil {
		customServerURL = *a.CustomServerURL
	}

	return fmt.Sprintf("code: %s, url: %s", a.Code, customServerURL)
}

type Accounts struct {
	mu   sync.Mutex
	path string
	log  *slog.Logger
}

func NewAccounts(path string) *Accounts {
	return &Accounts{path: path}
}

func (a *Accounts) loadAll() ([]Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var accounts map[string]Account
	err := readJSON(a.path, &accounts)

	if err != nil {
		return nil, fmt.Errorf("error while reading json file, %w", err)
	}

	accountsSlice := slices.Collect(maps.Values(accounts))

	return accountsSlice, nil
}

func (a *Accounts) writeAll(accounts []Account) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	accountsMap := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		accountsMap[account.Code] = account
	}

	err := writeJSON(a.path, accountsMap)

	if err != nil {
		return fmt.Errorf("error while writing json file, %w", err)
	}

	return nil
}

func (a *Accounts) Get(code string) (Account, error) {
	accounts, err := a.loadAll()
	if err != nil {
		return Account{}, fmt.Errorf("error while loading accounts: %w", err)
	}

	for _, account := range accounts {
		if account.Code == code {
			return account, nil
		}
	}

	return Account{}, ErrNotFound
}

func (a *Accounts) Create(code string, serverUrl *string) (Account, error) {
	accounts, err := a.loadAll()
	if err != nil {
		return Account{}, fmt.Errorf("error while loading accounts: %w", err)
	}

	accountIndex := a.FindIndex(code, accounts)
	if accountIndex != -1 {
		return Account{}, ErrAlreadyExists
	}

	newAccount := Account{Code: code, CustomServerURL: serverUrl, CreatedAt: time.Now().String(), UpdatedAt: time.Now().String()}

	accounts = append(accounts, newAccount)

	err = a.writeAll(accounts)
	if err != nil {
		return Account{}, fmt.Errorf("error while saving account, %w", err)
	}

	return newAccount, nil
}

func (a *Accounts) FindIndex(code string, accounts []Account) int {
	if len(accounts) == 0 {
		return -1
	}

	for i, account := range accounts {
		if account.Code == code {
			return i
		}
	}

	return -1
}

func (a *Accounts) Update(code string, serverUrl *string) (Account, error) {
	accounts, err := a.loadAll()
	if err != nil {
		return Account{}, fmt.Errorf("error while loading accounts: %w", err)
	}

	account, err := a.Get(code)
	if errors.Is(err, ErrNotFound) {
		return Account{}, fmt.Errorf("error while updating account: %w", err)
	}

	account.UpdatedAt = time.Now().String()
	account.CustomServerURL = serverUrl

	accountIndex := a.FindIndex(code, accounts)
	if accountIndex == -1 {
		return Account{}, fmt.Errorf("error while updating account: %w", err)
	}

	accounts[accountIndex] = account

	err = a.writeAll(accounts)
	if err != nil {
		return Account{}, fmt.Errorf("error while saving account, %w", err)
	}

	return account, nil
}

func (a *Accounts) GetOrCreate(code string) (Account, error) {
	account, err := a.Create(code, nil)

	if err != nil && !errors.Is(err, ErrAlreadyExists) {
		return Account{}, fmt.Errorf("error while creating account: %w", err)
	}

	if errors.Is(err, ErrAlreadyExists) {
		account, err = a.Get(code)
		if err != nil {
			return Account{}, fmt.Errorf("error while searching for account: %w", err)
		}
	}

	return account, nil
}

func (a *Accounts) UpdateServerURL(code, serverUrl string) error {
	_, err := a.Update(code, new(serverUrl))
	if err != nil {
		return fmt.Errorf("error while updating server url: %w", err)
	}

	return nil
}
