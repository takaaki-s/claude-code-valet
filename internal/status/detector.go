package status

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Debug flag - set CCVALET_DEBUG=1 to enable
var debugEnabled = os.Getenv("CCVALET_DEBUG") == "1"
var debugLogPath string

// Regex patterns for filtering
var (
	// ANSI escape sequences
	ansiEscapeRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07`)
	// Status bar has │ separators and time pattern (HH:MM:SS)
	statusBarRegex = regexp.MustCompile(`│.*│.*\d{2}:\d{2}:\d{2}`)
	// Box decorations: ╭╮╰╯─ patterns (including lines with only ─)
	boxDecorationRegex = regexp.MustCompile(`^[\s]*(╭|╰|├|└|─).*─|─.*(╮|╯|┤|┘)[\s]*$|^[\s]*─+[\s]*$`)
)

func init() {
	if debugEnabled {
		home, _ := os.UserHomeDir()
		debugLogPath = filepath.Join(home, ".ccvalet", "debug.log")
	}
}

func debugLog(format string, args ...interface{}) {
	if !debugEnabled || debugLogPath == "" {
		return
	}
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
}

// Detector detects session status from PTY output
type Detector struct {
	patterns []Pattern
}

// NewDetector creates a new status detector with default patterns
func NewDetector() *Detector {
	return &Detector{
		patterns: DefaultPatterns(),
	}
}

// stripAnsi removes ANSI escape sequences from text
func stripAnsi(text string) string {
	return ansiEscapeRegex.ReplaceAllString(text, "")
}

// processCarriageReturn handles carriage return (\r) by keeping only the last segment
// This simulates terminal behavior where \r moves cursor to beginning of line
func processCarriageReturn(line string) string {
	// Split by \r and take the last non-empty segment
	parts := strings.Split(line, "\r")
	for i := len(parts) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(parts[i])
		if trimmed != "" {
			return parts[i]
		}
	}
	return ""
}

// filterLines removes status bar and box decoration lines
// Returns: recentLines (last 30 lines), lastFewLines (last 5 lines), lastContentLine
func filterLines(text string) (recentLines string, lastFewLines string, lastContentLine string) {
	// First, strip ANSI escape sequences
	cleanText := stripAnsi(text)

	lines := strings.Split(cleanText, "\n")
	var filtered []string

	for _, line := range lines {
		// Process carriage returns to get the actual visible content
		line = processCarriageReturn(line)

		// Skip empty lines
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Skip status bar lines
		if statusBarRegex.MatchString(line) {
			continue
		}
		// Skip box decoration lines
		if boxDecorationRegex.MatchString(line) {
			continue
		}
		filtered = append(filtered, line)
	}

	// Get last 30 lines for recent context
	start := 0
	if len(filtered) > 30 {
		start = len(filtered) - 30
	}
	recentLines = strings.Join(filtered[start:], "\n")

	// Get last 2 lines for thinking detection during idle check
	// Claude Code alternates between spinner lines and prompt lines,
	// so we only need to check the most recent lines
	start2 := 0
	if len(filtered) > 2 {
		start2 = len(filtered) - 2
	}
	lastFewLines = strings.Join(filtered[start2:], "\n")

	// Get last content line for idle detection
	if len(filtered) > 0 {
		lastContentLine = filtered[len(filtered)-1]
	}

	return recentLines, lastFewLines, lastContentLine
}

// Detect analyzes the output text and returns the detected status.
// Returns empty string if no pattern matches.
func (d *Detector) Detect(text string) DetectedStatus {
	// Filter out status bar and box decorations
	recentLines, lastFewLines, lastContentLine := filterLines(text)

	if debugEnabled {
		debugLog("Last few lines (2): %q", lastFewLines)
		debugLog("Last content line: %q", lastContentLine)
		// Check for idle patterns in raw text (before filtering)
		cleanText := stripAnsi(text)
		if strings.Contains(cleanText, "❯") {
			debugLog("Raw text contains ❯ prompt")
		}
		// Show last 200 chars of clean text for debugging
		if len(cleanText) > 200 {
			debugLog("Last 200 chars: %q", cleanText[len(cleanText)-200:])
		} else {
			debugLog("Clean text: %q", cleanText)
		}
	}

	// Check patterns in priority order: permission > thinking > error > idle
	// - permission, trust: use recentLines (30 lines)
	// - thinking: use lastFewLines (2 lines) to avoid stale spinner messages
	// - error: use lastFewLines (2 lines) to avoid false positives from code output
	// - idle: use lastFewLines (2 lines) because prompt line may be followed by "? for shortcuts"
	for _, p := range d.patterns {
		if p.Status == StatusIdle {
			// まず最終行でidle検出を試みる（最優先）
			// 最終行にプロンプトがあればidle確定（上の行に"esc to interrupt"が残っていても）
			for _, pattern := range p.Patterns {
				if strings.Contains(lastContentLine, pattern) {
					debugLog("Detected %s (pattern: %q in last content line)", p.Status, pattern)
					return p.Status
				}
			}
			// 最終行にプロンプトがない場合、lastFewLinesでチェック
			// ただし "esc to interrupt" がある場合はthinkingと判定させるためスキップ
			if strings.Contains(lastFewLines, "esc to interrupt") {
				continue
			}
			for _, pattern := range p.Patterns {
				if strings.Contains(lastFewLines, pattern) {
					debugLog("Detected %s (pattern: %q in last few lines)", p.Status, pattern)
					return p.Status
				}
			}
			continue
		}

		if p.Status == StatusThinking {
			// Thinking detection: use lastFewLines (last 2 lines) only
			// This prevents false positives from old spinner messages in the buffer
			for _, pattern := range p.Patterns {
				if strings.Contains(lastFewLines, pattern) {
					debugLog("Detected %s (pattern: %q in last 2 lines)", p.Status, pattern)
					return p.Status
				}
			}
			continue
		}

		if p.Status == StatusError {
			// Error detection: use lastFewLines (last 2 lines) only
			// This prevents false positives from error messages in code output
			// (e.g., Claude showing error logs, discussing errors, etc.)
			//
			// Skip error detection if idle prompt is present
			// Claude may discuss errors while waiting for input
			hasIdlePrompt := strings.Contains(lastFewLines, "❯") || strings.Contains(lastFewLines, "> ")
			if hasIdlePrompt {
				debugLog("Skipping error detection: idle prompt found")
				continue
			}
			for _, pattern := range p.Patterns {
				if strings.Contains(lastFewLines, pattern) {
					debugLog("Detected %s (pattern: %q in last 2 lines)", p.Status, pattern)
					return p.Status
				}
			}
			continue
		}

		// Other statuses (permission, trust): check recent lines (30 lines)
		for _, pattern := range p.Patterns {
			if strings.Contains(recentLines, pattern) {
				debugLog("Detected %s (pattern: %q)", p.Status, pattern)
				return p.Status
			}
		}
	}

	debugLog("No pattern matched")
	return ""
}
