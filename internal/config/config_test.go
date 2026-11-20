package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"live-recorder/internal/domain"
)

func TestReadUsers(t *testing.T) {
	t.Run("admite espacios, tabulaciones, comentarios y líneas vacías", func(t *testing.T) {
		text := "# usuarios\n\n123   Ana\ttrue\n456 Beto false\n"
		users, err := ReadUsers(text)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(users) != 2 {
			t.Fatalf("expected 2 users, got %d", len(users))
		}
		if users[0].ID != "123" || users[0].Alias != "Ana" || !users[0].ShouldRecord {
			t.Errorf("unexpected user 0: %+v", users[0])
		}
		if users[1].ID != "456" || users[1].Alias != "Beto" || users[1].ShouldRecord {
			t.Errorf("unexpected user 1: %+v", users[1])
		}
	})

	t.Run("rechaza configuración ambigua con número de línea", func(t *testing.T) {
		invalidCases := []string{
			"123 Ana",
			"123 Ana yes",
			"abc Ana true",
			"123 Ana María true",
		}
		for _, line := range invalidCases {
			_, err := ReadUsers("# comentario\n" + line)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", line)
			}
			if !strings.Contains(err.Error(), "Línea 2") {
				t.Errorf("expected error to contain 'Línea 2', got: %v", err)
			}
		}

		_, err := ReadUsers("123 Ana true\n123 Beto false")
		if err == nil {
			t.Fatalf("expected error for duplicate ID, got nil")
		}
		if !strings.Contains(err.Error(), "ID duplicado") {
			t.Errorf("expected error to contain 'ID duplicado', got: %v", err)
		}
	})
}

func TestParseInterval(t *testing.T) {
	val, err := ParseInterval("")
	if err != nil || val != 120 {
		t.Errorf("expected 120, got %d (err: %v)", val, err)
	}

	val, err = ParseInterval("30")
	if err != nil || val != 30 {
		t.Errorf("expected 30, got %d (err: %v)", val, err)
	}

	invalid := []string{"0", "-1", "1.5", "abc", "900719925474099299999999999999999999999999999"}
	for _, v := range invalid {
		_, err := ParseInterval(v)
		if err == nil {
			t.Errorf("expected error for invalid interval %q", v)
		}
	}
}

func TestCreateOutputPath(t *testing.T) {
	user := domain.UserConfig{ID: "123", Alias: "../../Ana", ShouldRecord: true}
	first := CreateOutputPath("/tmp/videos", user)
	if filepath.Dir(first) != "/tmp/videos" {
		t.Errorf("expected dir /tmp/videos, got %s", filepath.Dir(first))
	}
	if !strings.Contains(first, "_123_") {
		t.Errorf("expected output to contain user ID '_123_', got %s", first)
	}
	second := CreateOutputPath("/tmp/videos", user)
	if first == second {
		t.Errorf("expected unique output paths, but got identical: %s", first)
	}
}

func TestSaveUsersAtomic(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "user_list.txt")

	users := []domain.UserConfig{
		{ID: "111", Alias: "Alice", ShouldRecord: true},
		{ID: "222", Alias: "Bob", ShouldRecord: false},
	}
	if err := SaveUsersAtomic(filePath, users); err != nil {
		t.Fatalf("SaveUsersAtomic failed: %v", err)
	}

	readBack, err := ReadUsersFromFile(filePath)
	if err != nil {
		t.Fatalf("ReadUsersFromFile failed: %v", err)
	}
	if len(readBack) != 2 {
		t.Fatalf("expected 2 users, got %d", len(readBack))
	}
	if readBack[0] != users[0] || readBack[1] != users[1] {
		t.Errorf("read users do not match saved users")
	}

	// Verify file content starts with # id alias record
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if !strings.HasPrefix(string(content), "# id alias record\n") {
		t.Errorf("expected header comment, got: %s", string(content))
	}
}
