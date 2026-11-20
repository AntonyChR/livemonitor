package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"live-recorder/internal/domain"
)

// ReadUsers parses the content of a user_list.txt file.
// Matches exact validation rules from the TypeScript implementation:
// - Ignores empty lines and comments (#)
// - Splits each line by whitespace
// - Expects exactly: id alias true|false
// - ID must be numeric digits only
// - Flags must be strictly "true" or "false"
// - Disallows duplicate IDs
func ReadUsers(text string) ([]domain.UserConfig, error) {
	lines := strings.Split(text, "\n")
	userMap := make(map[string]domain.UserConfig)
	var users []domain.UserConfig

	for index, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("Línea %d: se esperaba \"id alias true|false\" (alias sin espacios)", index+1)
		}
		id := fields[0]
		alias := fields[1]
		flag := fields[2]

		// Check id contains only digits
		if !isAllDigits(id) || alias == "" || (flag != "true" && flag != "false") {
			return nil, fmt.Errorf("Línea %d: se esperaba \"id alias true|false\" (alias sin espacios)", index+1)
		}

		if _, exists := userMap[id]; exists {
			return nil, fmt.Errorf("Línea %d: ID duplicado %s", index+1, id)
		}

		user := domain.UserConfig{
			ID:           id,
			Alias:        alias,
			ShouldRecord: flag == "true",
		}
		userMap[id] = user
		users = append(users, user)
	}

	return users, nil
}

// ReadUsersFromFile reads and parses user list from a file.
func ReadUsersFromFile(filePath string) ([]domain.UserConfig, error) {
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return ReadUsers(string(bytes))
}

// SaveUsersAtomic writes the user configs to a file atomically using a temporary file.
func SaveUsersAtomic(filePath string, users []domain.UserConfig) error {
	dir := filepath.Dir(filePath)
	tempFile, err := os.CreateTemp(dir, "user_list_*.tmp")
	if err != nil {
		return fmt.Errorf("error al crear archivo temporal para guardar usuarios: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	writer := bufio.NewWriter(tempFile)
	if _, err := writer.WriteString("# id alias record\n"); err != nil {
		tempFile.Close()
		return err
	}

	for _, user := range users {
		flag := "false"
		if user.ShouldRecord {
			flag = "true"
		}
		if _, err := fmt.Fprintf(writer, "%s %s %s\n", user.ID, user.Alias, flag); err != nil {
			tempFile.Close()
			return err
		}
	}

	if err := writer.Flush(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		return fmt.Errorf("error al reemplazar archivo de usuarios: %w", err)
	}
	return nil
}

// ValidateUser checks that a user can be written to and read back from
// the user list file. Aliases cannot contain whitespace because fields are
// separated by spaces.
func ValidateUser(user domain.UserConfig) error {
	if !isAllDigits(strings.TrimSpace(user.ID)) {
		return fmt.Errorf("el ID de usuario debe contener solo dígitos")
	}
	alias := strings.TrimSpace(user.Alias)
	if alias == "" {
		return fmt.Errorf("el alias no puede estar vacío")
	}
	if strings.ContainsAny(alias, " \t\r\n") {
		return fmt.Errorf("el alias no puede contener espacios en blanco")
	}
	return nil
}

func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
