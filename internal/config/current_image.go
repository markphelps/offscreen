package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2/unstable"
)

// SetCurrentImageFile updates only the top-level image reference, preserving
// comments and unrelated settings that older versions of offscreen don't know.
func SetCurrentImageFile(path, name string) error {
	if !utf8.ValidString(name) {
		return errors.New("image reference is not valid UTF-8")
	}
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
	updated, err := currentImageContents(contents, name)
	if err != nil {
		return err
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
	if _, err := file.Write(updated); err != nil {
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

// Use the syntax parser only to locate source ranges. The config decoder above
// still validates the document; unrelated values and comments are never encoded.
func currentImageContents(contents []byte, name string) ([]byte, error) {
	value, err := json.Marshal(name)
	if err != nil {
		return nil, fmt.Errorf("encode image reference: %w", err)
	}
	// JSON leaves DEL literal, but TOML basic strings require it escaped.
	value = bytes.ReplaceAll(value, []byte{0x7f}, []byte(`\u007f`))
	var parser unstable.Parser
	parser.Reset(contents)
	insertion := len(contents)
	for parser.NextExpression() {
		expression := parser.Expression()
		switch expression.Kind {
		case unstable.Table, unstable.ArrayTable:
			key := expression.Key()
			key.Next()
			// Table headers occupy their own line. Include indentation in the
			// untouched suffix instead of inserting inside the header.
			insertion = bytes.LastIndexByte(contents[:key.Node().Raw.Offset], '\n') + 1
			return insertCurrentImage(contents, insertion, value), nil
		case unstable.KeyValue:
			key := expression.Key()
			if key.Next() && key.IsLast() && bytes.Equal(key.Node().Data, []byte("current_image")) {
				raw := expression.Value().Raw
				start, end := int(raw.Offset), int(raw.Offset+raw.Length)
				updated := append(bytes.Clone(contents[:start]), value...)
				return append(updated, contents[end:]...), nil
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, fmt.Errorf("locate current image: %w", err)
	}
	return insertCurrentImage(contents, insertion, value), nil
}

func insertCurrentImage(contents []byte, offset int, value []byte) []byte {
	prefix := bytes.Clone(contents[:offset])
	if len(prefix) > 0 && prefix[len(prefix)-1] != '\n' {
		prefix = append(prefix, '\n')
	}
	prefix = append(prefix, "current_image = "...)
	prefix = append(prefix, value...)
	prefix = append(prefix, '\n')
	return append(prefix, contents[offset:]...)
}
