package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func EnvFilePath(configPath string) string {
	if override := os.Getenv("NEXUS_ENV_FILE"); override != "" {
		return override
	}

	dir := filepath.Dir(configPath)
	if dir == "." || dir == "" {
		return ".env"
	}

	return filepath.Join(dir, ".env")
}

func LoadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := parseEnvLine(scanner.Text())
		if !ok {
			continue
		}
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}

	return scanner.Err()
}

func SaveEnvValues(path string, values map[string]string) error {
	cleaned := map[string]string{}
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if !validEnvKey(key) {
			return fmt.Errorf("invalid environment key %q", key)
		}
		cleaned[key] = value
	}

	if len(cleaned) == 0 {
		return nil
	}

	lines := []string{}
	seen := map[string]bool{}

	bytes, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(bytes)))
		for scanner.Scan() {
			line := scanner.Text()
			key, _, ok := parseEnvLine(line)
			if ok {
				if value, exists := cleaned[key]; exists {
					lines = append(lines, key+"="+quoteEnvValue(value))
					seen[key] = true
					continue
				}
			}
			lines = append(lines, line)
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	}

	missing := make([]string, 0, len(cleaned))
	for key := range cleaned {
		if !seen[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)

	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" && len(missing) > 0 {
		lines = append(lines, "")
	}
	for _, key := range missing {
		lines = append(lines, key+"="+quoteEnvValue(cleaned[key]))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}

	data := strings.Join(lines, "\n")
	if data != "" {
		data += "\n"
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ApplyEnvValues(values map[string]string) error {
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func parseEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	line = strings.TrimPrefix(line, "export ")
	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}

	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if !validEnvKey(key) {
		return "", "", false
	}

	return key, unquoteEnvValue(value), true
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}

	for index, char := range key {
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' {
			continue
		}
		if index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}

	return true
}

func unquoteEnvValue(value string) string {
	if len(value) < 2 {
		return value
	}

	quote := value[0]
	if quote != '"' && quote != '\'' || value[len(value)-1] != quote {
		return value
	}

	value = value[1 : len(value)-1]
	if quote == '"' {
		value = strings.ReplaceAll(value, `\"`, `"`)
		value = strings.ReplaceAll(value, `\\`, `\`)
	}
	return value
}

func quoteEnvValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}
