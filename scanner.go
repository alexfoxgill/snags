package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Marker is an inline comment request found in the working tree,
// e.g. `// snag: rename this function`.
type Marker struct {
	File    string // path relative to projectRoot
	Line    int    // 1-based line of the marker's first line
	Text    string // the request text (continuation lines joined with a space)
	Context string // ~15-line snippet of surrounding code
}

const contextRadius = 7

// commentClosers maps block-comment leaders to their closers. Leaders absent
// from this map are line comments.
var commentClosers = map[string]string{"/*": "*/", "<!--": "-->"}

// markerRegexp matches a comment leader followed by the keyword and a colon.
// The leader may be preceded by code (trailing comment).
func markerRegexp(keyword string) *regexp.Regexp {
	return regexp.MustCompile(`(<!--|//|/\*|#|--)[ \t]*` + regexp.QuoteMeta(keyword) + `:[ \t]*`)
}

// fileMarker is a parsed marker with the positions needed for deletion.
// Line indices are 0-based.
type fileMarker struct {
	startLine int // marker line
	endLine   int // last continuation line (== startLine if none)
	text      string
	leaderPos int // byte offset of the leader on the marker line
	spanEnd   int // byte offset just past the comment span on the marker line
}

// parseMarkers finds all markers in lines using a regexp from markerRegexp.
// Line-comment markers consume immediately-following full-line comments with
// the same leader as continuations; block-comment markers are single-line.
func parseMarkers(lines []string, re *regexp.Regexp) []fileMarker {
	var markers []fileMarker
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		m := re.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		leader := line[m[2]:m[3]]
		closer := commentClosers[leader]
		text := line[m[1]:]
		spanEnd := len(line)
		if closer != "" {
			idx := strings.Index(text, closer)
			if idx < 0 {
				// Unclosed block comment: the comment spans further lines,
				// so deleting just this line would leave a dangling closer.
				// Not a marker.
				continue
			}
			spanEnd = m[1] + idx + len(closer)
			text = text[:idx]
		} else {
			// A line-comment leader inside a block comment, e.g.
			// `<!-- note -- snag: fix -->`: strip the block closer from the
			// text and end the span before it so deletion preserves it.
			for _, c := range []string{"*/", "-->"} {
				if t := strings.TrimRight(text, " \t"); strings.HasSuffix(t, c) {
					text = t[:len(t)-len(c)]
					spanEnd = m[1] + len(text)
				}
			}
		}
		text = strings.TrimSpace(text)

		start, end := i, i
		if closer == "" {
			for j := i + 1; j < len(lines); j++ {
				trimmed := strings.TrimLeft(lines[j], " \t")
				if !strings.HasPrefix(trimmed, leader) || re.MatchString(lines[j]) {
					break
				}
				cont := strings.TrimSpace(trimmed[len(leader):])
				if cont == "" {
					break
				}
				text += " " + cont
				end = j
			}
		}
		i = end

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		markers = append(markers, fileMarker{
			startLine: start,
			endLine:   end,
			text:      text,
			leaderPos: m[2],
			spanEnd:   spanEnd,
		})
	}
	return markers
}

// splitLines splits content into lines without a trailing empty element.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// ScanMarkers finds all `<keyword>:` comment markers in the working tree.
// git grep is only a prefilter for candidate files; line numbers and text
// come from parsing the files. Discovery is purely textual, so a marker
// inside a string literal is indistinguishable from one in a comment.
func ScanMarkers(projectRoot, keyword string) ([]Marker, error) {
	pattern := `(//|#|--|/\*|<!--)[[:space:]]*` + regexp.QuoteMeta(keyword) + `:`
	// -l -z prints NUL-separated file names without quoting, so paths with
	// non-ASCII characters (which core.quotePath mangles in line-oriented
	// output) or `:` parse correctly.
	cmd := exec.Command("git", "-C", projectRoot, "grep", "-lzIE", "--untracked", "-e", pattern, "--", ".")
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if ok && ee.ExitCode() == 1 && len(out) == 0 {
			return nil, nil
		}
		var detail string
		if ok {
			detail = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("git grep: %w%s", err, detail)
	}

	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	sort.Strings(files)

	re := markerRegexp(keyword)
	var result []Marker
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(projectRoot, f))
		if err != nil {
			return nil, err
		}
		lines := splitLines(string(data))
		for _, fm := range parseMarkers(lines, re) {
			start := max(0, fm.startLine-contextRadius)
			end := min(len(lines)-1, fm.endLine+contextRadius)
			result = append(result, Marker{
				File:    f,
				Line:    fm.startLine + 1,
				Text:    fm.text,
				Context: strings.Join(lines[start:end+1], "\n"),
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].File != result[j].File {
			return result[i].File < result[j].File
		}
		return result[i].Line < result[j].Line
	})
	return result, nil
}

// DeleteMarker removes the marker matching markerText from file in the
// working tree. It is a no-op when the marker is gone, the file is gone, or
// the marker is committed to HEAD (the agent's branch removal propagates via
// the merge; deleting locally would dirty the file and block it).
func DeleteMarker(projectRoot, file, markerText, keyword string) error {
	path := filepath.Join(projectRoot, file)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	content := string(data)
	lines := splitLines(content)

	re := markerRegexp(keyword)
	var target *fileMarker
	for _, fm := range parseMarkers(lines, re) {
		if fm.text == markerText {
			target = &fm
			break
		}
	}
	if target == nil {
		return nil
	}

	if head, err := exec.Command("git", "-C", projectRoot, "show", "HEAD:"+filepath.ToSlash(file)).Output(); err == nil {
		for _, fm := range parseMarkers(splitLines(string(head)), re) {
			if fm.text == markerText {
				return nil
			}
		}
	}

	out := append([]string{}, lines[:target.startLine]...)
	markerLine := lines[target.startLine]
	remainder := strings.TrimRight(markerLine[:target.leaderPos]+markerLine[target.spanEnd:], " \t")
	if strings.TrimSpace(remainder) != "" {
		out = append(out, remainder)
	}
	out = append(out, lines[target.endLine+1:]...)

	newContent := strings.Join(out, "\n")
	if len(out) > 0 && strings.HasSuffix(content, "\n") {
		newContent += "\n"
	}

	perm := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	return os.WriteFile(path, []byte(newContent), perm)
}
