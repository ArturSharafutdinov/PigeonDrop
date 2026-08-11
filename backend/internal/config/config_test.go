package config

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfig_Load_AllSet(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("STORAGE_DIR", "data")
	t.Setenv("DEFAULT_SERVER_URL", "https://test")

	config, err := Load()
	if err != nil {
		t.Errorf("error while loading config %v", err)
	}

	assert.Equal(t, 8080, config.Port)
	assert.Equal(t, "data", config.StorageDir)
	assert.Equal(t, "https://test", config.DefaultServerURL)
}

func TestConfig_Load_NoPort(t *testing.T) {
	t.Setenv("STORAGE_DIR", "data")
	t.Setenv("DEFAULT_SERVER_URL", "https://test")

	config, err := Load()
	if err != nil {
		t.Errorf("error while loading config %v", err)
	}

	assert.Equal(t, 3000, config.Port)
	assert.Equal(t, "data", config.StorageDir)
	assert.Equal(t, "https://test", config.DefaultServerURL)
}

func TestConfig_Load_AllNoSet(t *testing.T) {
	config, err := Load()
	if err != nil {
		t.Errorf("error while loading config %v", err)
	}

	wd, _ := os.Getwd()

	assert.Equal(t, 3000, config.Port)
	assert.Equal(t, wd, config.StorageDir)
	assert.Equal(t, fmt.Sprintf("http://localhost:%d", 3000), config.DefaultServerURL)
}

func TestConfig_Load_InvalidPortNumber(t *testing.T) {
	t.Setenv("PORT", "abc")

	_, err := Load()
	assert.EqualErrorf(t, err, "error while parsing port: strconv.Atoi: parsing \"abc\": invalid syntax", "", nil)
}

func TestConfig_Load_NotValidPortNumber(t *testing.T) {
	t.Setenv("PORT", "99999")

	_, err := Load()
	assert.EqualErrorf(t, err, "invalid port number: 99999", "", nil)
}
