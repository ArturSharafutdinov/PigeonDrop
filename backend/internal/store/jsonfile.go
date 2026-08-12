package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

func isFileExists(path string) bool {
	if path == "" {
		return false
	}

	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}

	return true
}

func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("error while reading file: %w", err)
	}

	err = json.Unmarshal(data, dst)
	if err != nil {
		return fmt.Errorf("error while unmarshaling data: %w", err)
	}

	return nil
}

func writeJSON(path string, v any) error {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("file not exists, %w", err)
	}

	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return fmt.Errorf("error while marshaling data: %w", err)
	}

	err = os.WriteFile(path, data, 0o644)
	if err != nil {
		return fmt.Errorf("error while writing file data: %w", err)
	}

	return nil
}
