package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitCommandFor maps an action id to its configured command line.
func gitCommandFor(cfg Config, op string) (string, error) {
	switch op {
	case "pull":
		return cfg.PullCommand, nil
	case "push":
		return cfg.PushCommand, nil
	}
	return "", fmt.Errorf("unknown git operation %q", op)
}

// resolveActionCwd picks the folder a pull/push should run in: the focused
// pane's cwd (what "the folder the tab is in" means in Herdr), falling back
// to a live pane lookup and then the workspace cwd.
func resolveActionCwd(ctx PluginContext) (string, error) {
	if cwd := cleanPath(ctx.FocusedPaneCwd); cwd != "" {
		return cwd, nil
	}
	if ctx.FocusedPaneID != "" {
		if pane, err := paneGet(ctx.FocusedPaneID); err == nil {
			if pane.ForegroundCwd != nil && cleanPath(*pane.ForegroundCwd) != "" {
				return cleanPath(*pane.ForegroundCwd), nil
			}
			if pane.Cwd != nil && cleanPath(*pane.Cwd) != "" {
				return cleanPath(*pane.Cwd), nil
			}
		}
	}
	if cwd := cleanPath(ctx.WorkspaceCwd); cwd != "" {
		return cwd, nil
	}
	return "", errors.New("could not determine the focused pane's folder")
}

// cleanPath trims whitespace and a trailing separator (Herdr reports some
// Windows cwds as "C:\dir\"), keeping drive roots like "C:\" intact.
func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if len(p) > 3 {
		p = strings.TrimRight(p, `\/`)
	}
	return p
}

// runGitAction is the `pull` / `push` action entry point.
func runGitAction(cfg Config, op string) error {
	ctx := loadContext()
	cwd, err := resolveActionCwd(ctx)
	if err != nil {
		notify("git "+op+" failed", err.Error())
		return err
	}
	command, err := gitCommandFor(cfg, op)
	if err != nil {
		return err
	}
	if cfg.Output == "notification" {
		return runGitNotify(op, command, cwd)
	}
	if err := openGitPane(op, cwd); err != nil {
		// Popup unavailable (older Herdr, headless...): degrade gracefully.
		logf("popup failed (%v); falling back to notification", err)
		return runGitNotify(op, command, cwd)
	}
	return nil
}

// runGitNotify runs the git command in the background and reports the
// outcome through a Herdr notification.
func runGitNotify(op, command, cwd string) error {
	out, err := runShellCommand(command, cwd)
	tail := lastLines(out, 4)
	if err != nil {
		notify(fmt.Sprintf("git %s failed in %s", op, shortDir(cwd)), tail)
		return fmt.Errorf("%s in %s: %w\n%s", command, cwd, err, out)
	}
	if tail == "" {
		tail = "done"
	}
	notify(fmt.Sprintf("git %s: %s", op, shortDir(cwd)), tail)
	return nil
}

// runGitPane is the body of the popup pane: it runs the command with the
// terminal attached so progress, credential prompts, and colors all work.
func runGitPane(cfg Config) error {
	op := os.Getenv("HERDR_TABEXEC_OP")
	cwd := cleanPath(os.Getenv("HERDR_TABEXEC_CWD"))
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	command, err := gitCommandFor(cfg, op)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		waitForEnter()
		return err
	}
	fmt.Printf("\x1b[1m$ %s\x1b[0m  \x1b[2m(%s)\x1b[0m\n\n", command, cwd)

	argv, err := splitCommand(command)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		waitForEnter()
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=1")
	err = cmd.Run()
	if err != nil {
		fmt.Printf("\n\x1b[31mgit %s failed: %v\x1b[0m\n", op, err)
		waitForEnter()
		return err
	}
	fmt.Printf("\n\x1b[32mgit %s finished\x1b[0m", op)
	if cfg.PopupCloseDelay <= 0 {
		fmt.Println()
		waitForEnter()
		return nil
	}
	fmt.Printf(" (closing in %.0fs, Enter to close now)\n", cfg.PopupCloseDelay)
	waitForEnterOrTimeout(time.Duration(cfg.PopupCloseDelay * float64(time.Second)))
	return nil
}

func waitForEnter() {
	fmt.Print("Press Enter to close...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func waitForEnterOrTimeout(d time.Duration) {
	done := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// runShellCommand runs a command line (split shell-style, no shell involved)
// in dir and returns its combined output.
func runShellCommand(command, dir string) (string, error) {
	argv, err := splitCommand(command)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// splitCommand splits a command line on whitespace, honoring single and
// double quotes. Inside double quotes only \" and \\ are escapes, so plain
// Windows paths like "C:\Program Files\Git\git.exe" work unchanged.
func splitCommand(s string) ([]string, error) {
	var (
		args  []string
		cur   strings.Builder
		have  bool
		quote byte
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
			have = true
		case c == ' ' || c == '\t':
			if have {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in command %q", s)
	}
	if have {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return args, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func shortDir(p string) string {
	p = cleanPath(p)
	if i := strings.LastIndexAny(p, `\/`); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}
