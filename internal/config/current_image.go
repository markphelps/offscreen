package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	currentImageLine = regexp.MustCompile(`(?m)^[ \t]*(?:current_image|"current_image"|'current_image')[ \t]*=[ \t]*[^\n]*\n?`)
	firstTable       = regexp.MustCompile(`(?m)^\s*\[`)
)

// SetCurrentImageFile updates only the top-level image reference, preserving
// comments and unrelated settings that older versions of offscreen don't know.
func SetCurrentImageFile(path, name string) error {
	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg := Default()
		cfg.CurrentImage = name
		return SaveFile(path, cfg)
	}
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	// Validate first; do not rewrite a malformed user config.
	if _, err := LoadFile(path); err != nil {
		return err
	}
	body := string(contents)
	index := firstTable.FindStringIndex(body)
	if index == nil {
		index = []int{len(body), len(body)}
	}
	prefix, tables := body[:index[0]], body[index[0]:]
	line := "current_image = " + strconv.Quote(name) + "\n"
	if currentImageLine.MatchString(prefix) {
		prefix = currentImageLine.ReplaceAllString(prefix, line)
	} else {
		if prefix != "" && !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		prefix += line
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.WriteString(prefix + tables); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return nil
}
