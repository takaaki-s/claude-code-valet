package cmd

import (
	"os"
	"path/filepath"
)

// getConfigDir returns the configuration directory path
func getConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccvalet")
}
