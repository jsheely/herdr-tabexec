package main

import (
	"reflect"
	"testing"
)

func TestParseSimpleTOML(t *testing.T) {
	src := `# comment
popup_close_delay = 2.5 # trailing comment
flag = true

[[tools]]
tab_names = ["git", "Repo"]
command = "tt-git -p \"a b\""

[[tools]]
tab_names = ["mon"]
command = "btop"
detect = ["btop", "top"]
`
	got, tables, err := parseSimpleTOML(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"popup_close_delay": 2.5,
		"flag":              true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	wantTools := []map[string]any{
		{"tab_names": []string{"git", "Repo"}, "command": `tt-git -p "a b"`},
		{"tab_names": []string{"mon"}, "command": "btop", "detect": []string{"btop", "top"}},
	}
	if !reflect.DeepEqual(tables["tools"], wantTools) {
		t.Fatalf("tools: got %#v, want %#v", tables["tools"], wantTools)
	}
}

func TestParseSimpleTOMLRejectsPlainTables(t *testing.T) {
	if _, _, err := parseSimpleTOML("[keys]\nx = 1\n"); err == nil {
		t.Fatal("expected error for table header")
	}
}

func TestToolForTab(t *testing.T) {
	cfg := Config{Tools: []Tool{
		{TabNames: []string{"git", "Repo"}, Command: "tt-git"},
		{TabNames: []string{"mon"}, Command: "btop"},
		{TabNames: []string{"git"}, Command: "lazygit"},
	}}
	for label, want := range map[string]string{"git": "tt-git", " GIT ": "tt-git", "repo": "tt-git", "Mon": "btop"} {
		tool, ok := cfg.toolForTab(label)
		if !ok || tool.Command != want {
			t.Errorf("%q: got %q (%v), want %q", label, tool.Command, ok, want)
		}
	}
	for _, l := range []string{"gitx", "1", ""} {
		if _, ok := cfg.toolForTab(l); ok {
			t.Errorf("%q should not match", l)
		}
	}
}

func TestDetectNames(t *testing.T) {
	if got := (Tool{Command: `"C:\bin\TT-Git.exe" --x`}).detectNames(); !reflect.DeepEqual(got, []string{"tt-git"}) {
		t.Errorf("default: got %q", got)
	}
	if got := (Tool{Command: "btop", Detect: []string{" BTop ", ""}}).detectNames(); !reflect.DeepEqual(got, []string{"btop"}) {
		t.Errorf("explicit: got %q", got)
	}
}

func TestSplitCommand(t *testing.T) {
	cases := map[string][]string{
		`git pull`:                            {"git", "pull"},
		`git pull --rebase origin main`:       {"git", "pull", "--rebase", "origin", "main"},
		`"C:\Program Files\Git\git.exe" push`: {`C:\Program Files\Git\git.exe`, "push"},
		`git commit -m 'hello world'`:         {"git", "commit", "-m", "hello world"},
		`echo "a \"b\" c"`:                    {"echo", `a "b" c`},
	}
	for in, want := range cases {
		got, err := splitCommand(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	if _, err := splitCommand(`git "unterminated`); err == nil {
		t.Error("expected error for unterminated quote")
	}
}

func TestCleanPath(t *testing.T) {
	cases := map[string]string{
		`C:\Users\jon\proj\`: `C:\Users\jon\proj`,
		`C:\`:                `C:\`,
		`/home/jon/proj/`:    `/home/jon/proj`,
		`/`:                  `/`,
		`  /x/y  `:           `/x/y`,
	}
	for in, want := range cases {
		if got := cleanPath(in); got != want {
			t.Errorf("cleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}
