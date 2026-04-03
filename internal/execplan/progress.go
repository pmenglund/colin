package execplan

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrMissingProgressSection = errors.New("missing_progress_section")
	ErrMissingProgressItems   = errors.New("missing_progress_items")
)

var checklistPattern = regexp.MustCompile(`^\s*[-*]\s+\[([ xX])\]\s+(.*?)\s*$`)

// ProgressItem is one checkbox item inside an ExecPlan Progress section.
type ProgressItem struct {
	Text      string
	Completed bool
}

// Progress captures the parsed checklist items from an ExecPlan Progress section.
type Progress struct {
	Items []ProgressItem
}

// AllCompleted reports whether every parsed Progress item is checked off.
func (p Progress) AllCompleted() bool {
	if len(p.Items) == 0 {
		return false
	}
	for _, item := range p.Items {
		if !item.Completed {
			return false
		}
	}
	return true
}

// Remaining returns the unchecked Progress item text in order.
func (p Progress) Remaining() []string {
	remaining := make([]string, 0, len(p.Items))
	for _, item := range p.Items {
		if !item.Completed {
			remaining = append(remaining, item.Text)
		}
	}
	return remaining
}

// ParseProgress extracts checkbox items from the ExecPlan Progress section only.
func ParseProgress(body string) (Progress, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	inProgress := false
	items := make([]ProgressItem, 0)

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if heading, ok := headingTitle(line); ok {
			if strings.EqualFold(heading, "Progress") {
				inProgress = true
				continue
			}
			if inProgress {
				break
			}
			continue
		}
		if !inProgress {
			continue
		}
		match := checklistPattern.FindStringSubmatch(raw)
		if len(match) == 0 {
			continue
		}
		items = append(items, ProgressItem{
			Text:      strings.TrimSpace(match[2]),
			Completed: strings.EqualFold(match[1], "x"),
		})
	}

	if !containsProgressHeading(lines) {
		return Progress{}, ErrMissingProgressSection
	}
	if len(items) == 0 {
		return Progress{}, ErrMissingProgressItems
	}
	return Progress{Items: items}, nil
}

func containsProgressHeading(lines []string) bool {
	for _, raw := range lines {
		if heading, ok := headingTitle(strings.TrimSpace(raw)); ok && strings.EqualFold(heading, "Progress") {
			return true
		}
	}
	return false
}

func headingTitle(line string) (string, bool) {
	if !strings.HasPrefix(line, "#") {
		return "", false
	}
	title := strings.TrimLeft(line, "#")
	title = strings.TrimSpace(title)
	if title == "" {
		return "", false
	}
	return title, true
}

// WorkingCopy is a temporary ExecPlan file used during implementation turns.
type WorkingCopy struct {
	dir  string
	path string
}

// NewWorkingCopy materializes the current ExecPlan into a temporary markdown file.
func NewWorkingCopy(body string) (*WorkingCopy, error) {
	dir, err := os.MkdirTemp("", "colin-execplan-*")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "execplan.md")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &WorkingCopy{dir: dir, path: path}, nil
}

// Path returns the working-copy file path.
func (w *WorkingCopy) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

// ReadBody returns the current working-copy file contents.
func (w *WorkingCopy) ReadBody() (string, error) {
	if w == nil {
		return "", errors.New("nil working copy")
	}
	data, err := os.ReadFile(w.path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Close removes the temporary working-copy directory.
func (w *WorkingCopy) Close() error {
	if w == nil || w.dir == "" {
		return nil
	}
	return os.RemoveAll(w.dir)
}
