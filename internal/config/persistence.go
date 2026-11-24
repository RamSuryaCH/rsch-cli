package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

func updateCfgFile(updateCfg func(config *Config)) error {
	if cfg == nil {
		return errors.New("config not loaded")
	}
	configFile := viper.ConfigFileUsed()
	allowCreate := configFile == ""
	if allowCreate {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		configFile = filepath.Join(homeDir, appName+".json")
	}
	configData := []byte(`{}`)
	if info, err := os.Lstat(configFile); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("config file must be a regular file, not a symbolic link")
		}
		configData, err = os.ReadFile(configFile)
		if err != nil {
			return fmt.Errorf("failed to read config file: %w", err)
		}
	} else if !allowCreate || !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect config file: %w", err)
	}

	var current map[string]json.RawMessage
	if err := json.Unmarshal(configData, &current); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}
	if current == nil {
		return errors.New("config file must contain a JSON object")
	}
	var userCfg Config
	if err := json.Unmarshal(configData, &userCfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}
	before, err := json.Marshal(userCfg)
	if err != nil {
		return fmt.Errorf("failed to snapshot config: %w", err)
	}
	updateCfg(&userCfg)
	after, err := json.Marshal(userCfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	// Preserve unknown fields, spelling, and explicit zero values. Serializing
	// the entire typed Config would discard trust declarations and settings
	// owned by newer versions, even when only a theme or model changed.
	updatedData, err := applyConfigChanges(configData, before, after)
	if err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, updatedData, "", "  "); err != nil {
		return err
	}
	return writeConfigAtomically(configFile, formatted.Bytes())
}

func applyConfigChanges(current, before, after []byte) ([]byte, error) {
	var oldFields, newFields map[string]json.RawMessage
	if json.Unmarshal(before, &oldFields) != nil || json.Unmarshal(after, &newFields) != nil || newFields == nil {
		return after, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(current, &fields) != nil || fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	keys := make(map[string]struct{}, len(oldFields)+len(newFields))
	for key := range oldFields {
		keys[key] = struct{}{}
	}
	for key := range newFields {
		keys[key] = struct{}{}
	}
	for key := range keys {
		oldValue, newValue := oldFields[key], newFields[key]
		if bytes.Equal(oldValue, newValue) {
			continue
		}
		storedKey := key
		if _, exists := fields[storedKey]; !exists {
			for candidate := range fields {
				if strings.EqualFold(candidate, key) {
					storedKey = candidate
					break
				}
			}
		}
		if newValue == nil {
			delete(fields, storedKey)
			continue
		}
		if oldValue == nil {
			oldValue = []byte(`{}`)
		}
		value, err := applyConfigChanges(fields[storedKey], oldValue, newValue)
		if err != nil {
			return nil, err
		}
		fields[storedKey] = value
	}
	return json.Marshal(fields)
}

func writeConfigAtomically(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".rsch-config-*")
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("failed to sync config file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close config file: %w", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("failed to replace config file: %w", err)
	}
	return nil
}
