package main

import (
	"os"
	"strings"
	"testing"
)

func Test_run(t *testing.T) {
	dir := t.TempDir()
	args := []string{"--man-page", "--website", "--doc-path", dir}
	err := run(args)
	if err != nil {
		t.Fatalf("got error: %v", err)
	}

	manPage, err := os.ReadFile(dir + "/gm-issue-create.1")
	if err != nil {
		t.Fatalf("error reading `gm-issue-create.1`: %v", err)
	}
	if !strings.Contains(string(manPage), `\fBgm issue create`) {
		t.Fatal("man page corrupted")
	}

	markdownPage, err := os.ReadFile(dir + "/gm_issue_create.md")
	if err != nil {
		t.Fatalf("error reading `gm_issue_create.md`: %v", err)
	}
	if !strings.Contains(string(markdownPage), `## gm issue create`) {
		t.Fatal("markdown page corrupted")
	}
}
