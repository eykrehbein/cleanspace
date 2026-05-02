package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	scanDir, staleDays, sizeThreshold := parseArgs()

	p := tea.NewProgram(newModel(scanDir, staleDays, sizeThreshold), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func parseArgs() (string, int, int64) {
	scanDir := ""
	staleDays := 7
	var sizeThreshold int64 = 100 * 1024 * 1024 // 100 MB

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir", "-d":
			if i+1 < len(args) {
				scanDir = args[i+1]
				i++
			}
		case "--days":
			if i+1 < len(args) {
				if d, err := strconv.Atoi(args[i+1]); err == nil && d > 0 {
					staleDays = d
				}
				i++
			}
		case "--size":
			if i+1 < len(args) {
				if s, err := strconv.ParseInt(args[i+1], 10, 64); err == nil && s >= 0 {
					sizeThreshold = s * 1024 * 1024
				}
				i++
			}
		case "--help", "-h":
			printUsage()
			os.Exit(0)
		default:
			if !strings.HasPrefix(args[i], "-") && scanDir == "" {
				scanDir = args[i]
			}
		}
	}

	if scanDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: could not determine home directory: %v\n", err)
			os.Exit(1)
		}
		scanDir = home
	}

	scanDir = expandPath(scanDir)
	if info, err := os.Stat(scanDir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: %q is not a valid directory\n", scanDir)
		os.Exit(1)
	}

	return scanDir, staleDays, sizeThreshold
}

func expandPath(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func printUsage() {
	fmt.Print(`CleanSpace — Smart disk cleanup for developers

Usage:
  cleanspace [directory] [flags]

Arguments:
  directory    Directory to scan for stale projects & large files (default: your home directory)

Flags:
  -d, --dir    Directory to scan
  --days       Days of inactivity before a project is stale (default: 7)
  --size       Minimum file size in MB to flag as large (default: 100)
  -h, --help   Show this help

Examples:
  cleanspace                               (scan your home directory)
  cleanspace ~/development
  cleanspace --dir ~/projects --days 14
  cleanspace --dir ~/projects --size 500   (flag files >500 MB)
`)
}
