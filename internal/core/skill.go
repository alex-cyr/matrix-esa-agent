package core

import (
	"bytes"
	"fmt"
	"os"
)

// LoadSkill reads an agent system prompt from disk.
//
// A missing or empty skill file is a hard error. These paths resolve relative
// to the working directory, so a bad container layout or a renamed directory
// previously produced an agent with an empty system instruction -- which still
// returns plausible-looking text, making the failure invisible in the output.
func LoadSkill(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("load skill %q: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", fmt.Errorf("load skill %q: file is empty", path)
	}
	return string(data), nil
}
