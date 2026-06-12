package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initScannerRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "test@test.com")
	mustGit(t, dir, "config", "user.name", "Test")
	mustGit(t, dir, "commit", "--allow-empty", "-m", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readRepoFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustScan(t *testing.T, dir, keyword string) []Marker {
	t.Helper()
	markers, err := ScanMarkers(dir, keyword)
	if err != nil {
		t.Fatal(err)
	}
	return markers
}

func TestScanMarkersCommentStyles(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "a.go", "package a\n\n// snag: fix go\nfunc A() {}\n")
	writeRepoFile(t, dir, "b.py", "x = 1\n# snag: fix py\n")
	writeRepoFile(t, dir, "c.sql", "SELECT 1;\n-- snag: fix sql\n")
	writeRepoFile(t, dir, "d.c", "int x;\n/* snag: fix c */\n")
	writeRepoFile(t, dir, "e.html", "<p>hi</p>\n<!-- snag: fix html -->\n")
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-m", "files")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 5 {
		t.Fatalf("expected 5 markers, got %d: %+v", len(markers), markers)
	}
	want := []Marker{
		{File: "a.go", Line: 3, Text: "fix go"},
		{File: "b.py", Line: 2, Text: "fix py"},
		{File: "c.sql", Line: 2, Text: "fix sql"},
		{File: "d.c", Line: 2, Text: "fix c"},
		{File: "e.html", Line: 2, Text: "fix html"},
	}
	for i, w := range want {
		got := markers[i]
		if got.File != w.File || got.Line != w.Line || got.Text != w.Text {
			t.Errorf("marker %d: got %s:%d %q, want %s:%d %q",
				i, got.File, got.Line, got.Text, w.File, w.Line, w.Text)
		}
	}
}

func TestScanMarkersContinuation(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "main.go",
		"package main\n\n// snag: rename the helper\n// to something clearer\nfunc helper() {}\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d", len(markers))
	}
	if markers[0].Text != "rename the helper to something clearer" {
		t.Errorf("wrong text: %q", markers[0].Text)
	}
	if markers[0].Line != 3 {
		t.Errorf("expected line 3, got %d", markers[0].Line)
	}
}

func TestScanMarkersContinuationStops(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.go",
		"// snag: first\n//\n// unrelated\nx()\n// snag: second\n// more\ny()\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 2 {
		t.Fatalf("expected 2 markers, got %d: %+v", len(markers), markers)
	}
	if markers[0].Text != "first" {
		t.Errorf("blank comment should stop continuation, got %q", markers[0].Text)
	}
	if markers[1].Text != "second more" {
		t.Errorf("non-comment should stop continuation, got %q", markers[1].Text)
	}
}

func TestScanMarkersContinuationStopsAtMarker(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.py", "# snag: one\n# snag: two\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 2 {
		t.Fatalf("expected 2 markers, got %d: %+v", len(markers), markers)
	}
	if markers[0].Text != "one" || markers[0].Line != 1 {
		t.Errorf("got %q at line %d, want \"one\" at line 1", markers[0].Text, markers[0].Line)
	}
	if markers[1].Text != "two" || markers[1].Line != 2 {
		t.Errorf("got %q at line %d, want \"two\" at line 2", markers[1].Text, markers[1].Line)
	}
}

func TestScanMarkersBlockNoContinuation(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.c", "/* snag: block request */\n/* extra */\nint x;\n")
	writeRepoFile(t, dir, "f.html", "<!-- snag: html request -->\n<!-- more -->\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 2 {
		t.Fatalf("expected 2 markers, got %d: %+v", len(markers), markers)
	}
	if markers[0].Text != "block request" {
		t.Errorf("got %q, want \"block request\"", markers[0].Text)
	}
	if markers[1].Text != "html request" {
		t.Errorf("got %q, want \"html request\"", markers[1].Text)
	}
}

func TestScanMarkersTrailingComment(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.go", "x := 1 // snag: rename this\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d", len(markers))
	}
	if markers[0].Text != "rename this" || markers[0].Line != 1 {
		t.Errorf("got %q at line %d", markers[0].Text, markers[0].Line)
	}
}

func TestScanMarkersCustomKeyword(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.go", "// todo: do it\nx()\n// snag: not this\n")

	markers := mustScan(t, dir, "todo")
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d: %+v", len(markers), markers)
	}
	if markers[0].Text != "do it" {
		t.Errorf("got %q, want \"do it\"", markers[0].Text)
	}
}

func TestScanMarkersKeywordMetachars(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.go", "// fix.me: real\nx()\n// fixXme: decoy\n")

	markers := mustScan(t, dir, "fix.me")
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d: %+v", len(markers), markers)
	}
	if markers[0].Text != "real" {
		t.Errorf("got %q, want \"real\"", markers[0].Text)
	}
}

func TestScanMarkersNoMatches(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.go", "package main\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 0 {
		t.Errorf("expected no markers, got %+v", markers)
	}
}

func TestScanMarkersSorted(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "b.go", "// snag: b first\nx()\ny()\nz()\n// snag: b second\n")
	writeRepoFile(t, dir, "a.go", "x()\n// snag: a only\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 3 {
		t.Fatalf("expected 3 markers, got %d", len(markers))
	}
	want := []Marker{
		{File: "a.go", Line: 2},
		{File: "b.go", Line: 1},
		{File: "b.go", Line: 5},
	}
	for i, w := range want {
		if markers[i].File != w.File || markers[i].Line != w.Line {
			t.Errorf("marker %d: got %s:%d, want %s:%d",
				i, markers[i].File, markers[i].Line, w.File, w.Line)
		}
	}
}

func TestScanMarkersUntrackedAndIgnored(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, ".gitignore", "ignored.go\n")
	mustGit(t, dir, "add", ".gitignore")
	mustGit(t, dir, "commit", "-m", "gitignore")
	writeRepoFile(t, dir, "ignored.go", "// snag: hidden\n")
	writeRepoFile(t, dir, "untracked.go", "// snag: visible\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d: %+v", len(markers), markers)
	}
	if markers[0].File != "untracked.go" || markers[0].Text != "visible" {
		t.Errorf("got %s %q", markers[0].File, markers[0].Text)
	}
}

func TestScanMarkersContextClamped(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "small.go", "a()\n// snag: tiny\nb()\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d", len(markers))
	}
	if markers[0].Context != "a()\n// snag: tiny\nb()" {
		t.Errorf("wrong clamped context: %q", markers[0].Context)
	}
}

func TestScanMarkersContextWindow(t *testing.T) {
	dir := initScannerRepo(t)
	var lines []string
	for i := 1; i <= 20; i++ {
		if i == 10 {
			lines = append(lines, "// snag: mid")
		} else {
			lines = append(lines, "l"+string(rune('0'+i/10))+string(rune('0'+i%10)))
		}
	}
	writeRepoFile(t, dir, "big.go", strings.Join(lines, "\n")+"\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d", len(markers))
	}
	ctx := strings.Split(markers[0].Context, "\n")
	if len(ctx) != 15 {
		t.Fatalf("expected 15 context lines, got %d", len(ctx))
	}
	if ctx[0] != "l03" || ctx[14] != "l17" {
		t.Errorf("wrong context window: %q .. %q", ctx[0], ctx[14])
	}
}

func TestScanMarkersNonASCIIFilename(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "héllo.go", "package a\n// snag: fix accents\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d: %+v", len(markers), markers)
	}
	if markers[0].File != "héllo.go" || markers[0].Text != "fix accents" {
		t.Errorf("got %s %q", markers[0].File, markers[0].Text)
	}
}

func TestScanMarkersUnclosedBlock(t *testing.T) {
	dir := initScannerRepo(t)
	content := "/* snag: refactor\nthis thing */\nint x;\n"
	writeRepoFile(t, dir, "f.c", content)
	writeRepoFile(t, dir, "f.html", "<!-- snag: open\nrest -->\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 0 {
		t.Fatalf("unclosed block markers should be skipped, got %+v", markers)
	}

	if err := DeleteMarker(dir, "f.c", "refactor", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "f.c"); got != content {
		t.Errorf("file changed: %q", got)
	}
}

func TestScanMarkersEmptyText(t *testing.T) {
	dir := initScannerRepo(t)
	content := "x()\n// snag:\ny()\n"
	writeRepoFile(t, dir, "f.go", content)

	markers := mustScan(t, dir, "snag")
	if len(markers) != 0 {
		t.Fatalf("empty marker should be skipped, got %+v", markers)
	}
	if err := DeleteMarker(dir, "f.go", "", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "f.go"); got != content {
		t.Errorf("file changed: %q", got)
	}
}

func TestDeleteMarkerFullLineContinuation(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.go",
		"package main\n\n// snag: do the thing\n// across lines\nfunc f() {}\n")

	if err := DeleteMarker(dir, "f.go", "do the thing across lines", "snag"); err != nil {
		t.Fatal(err)
	}
	got := readRepoFile(t, dir, "f.go")
	if got != "package main\n\nfunc f() {}\n" {
		t.Errorf("wrong content after delete: %q", got)
	}
}

func TestDeleteMarkerTrailingComment(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.go", "x := 1 // snag: rename\ny := 2\n")

	if err := DeleteMarker(dir, "f.go", "rename", "snag"); err != nil {
		t.Fatal(err)
	}
	got := readRepoFile(t, dir, "f.go")
	if got != "x := 1\ny := 2\n" {
		t.Errorf("wrong content after delete: %q", got)
	}
}

func TestDeleteMarkerBlockWithRemainder(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.c", "foo(); /* snag: x */ bar();\n")

	if err := DeleteMarker(dir, "f.c", "x", "snag"); err != nil {
		t.Fatal(err)
	}
	got := readRepoFile(t, dir, "f.c")
	if got != "foo();  bar();\n" {
		t.Errorf("wrong content after delete: %q", got)
	}
}

func TestDeleteMarkerInsideBlockComment(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "f.html", "<!-- note -- snag: fix this -->\n<p>hi</p>\n")

	markers := mustScan(t, dir, "snag")
	if len(markers) != 1 || markers[0].Text != "fix this" {
		t.Fatalf("expected one marker %q, got %+v", "fix this", markers)
	}
	if err := DeleteMarker(dir, "f.html", "fix this", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "f.html"); got != "<!-- note -->\n<p>hi</p>\n" {
		t.Errorf("comment closer lost: %q", got)
	}
}

func TestDeleteMarkerNotFound(t *testing.T) {
	dir := initScannerRepo(t)
	content := "// snag: real\nx()\n"
	writeRepoFile(t, dir, "f.go", content)

	if err := DeleteMarker(dir, "f.go", "nonexistent", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "f.go"); got != content {
		t.Errorf("file changed: %q", got)
	}
}

func TestDeleteMarkerMissingFile(t *testing.T) {
	dir := initScannerRepo(t)
	if err := DeleteMarker(dir, "gone.go", "x", "snag"); err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
}

func TestDeleteMarkerHeadGuard(t *testing.T) {
	dir := initScannerRepo(t)
	content := "package a\n// snag: committed\nfunc f() {}\n"
	writeRepoFile(t, dir, "f.go", content)
	mustGit(t, dir, "add", "f.go")
	mustGit(t, dir, "commit", "-m", "with marker")

	if err := DeleteMarker(dir, "f.go", "committed", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "f.go"); got != content {
		t.Errorf("committed marker should not be deleted, got %q", got)
	}
}

func TestDeleteMarkerUncommitted(t *testing.T) {
	dir := initScannerRepo(t)
	head := "package a\nfunc f() {}\n"
	writeRepoFile(t, dir, "f.go", head)
	mustGit(t, dir, "add", "f.go")
	mustGit(t, dir, "commit", "-m", "clean")
	writeRepoFile(t, dir, "f.go", "package a\n// snag: new\nfunc f() {}\n")

	if err := DeleteMarker(dir, "f.go", "new", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "f.go"); got != head {
		t.Errorf("expected file restored to HEAD content, got %q", got)
	}
}

func TestDeleteMarkerUntrackedFile(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "new.go", "// snag: untracked\nx()\n")

	if err := DeleteMarker(dir, "new.go", "untracked", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "new.go"); got != "x()\n" {
		t.Errorf("wrong content after delete: %q", got)
	}
}

func TestDeleteMarkerTrailingNewline(t *testing.T) {
	dir := initScannerRepo(t)
	writeRepoFile(t, dir, "no_nl.go", "a()\n// snag: x")
	writeRepoFile(t, dir, "nl.go", "// snag: y\na()\n")

	if err := DeleteMarker(dir, "no_nl.go", "x", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "no_nl.go"); got != "a()" {
		t.Errorf("trailing newline added: %q", got)
	}

	if err := DeleteMarker(dir, "nl.go", "y", "snag"); err != nil {
		t.Fatal(err)
	}
	if got := readRepoFile(t, dir, "nl.go"); got != "a()\n" {
		t.Errorf("trailing newline lost: %q", got)
	}
}
