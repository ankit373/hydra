// SPDX-License-Identifier: MIT

package shellpath

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// A login shell is a subprocess and startup waits on it, so the budget is small
// and a slow shell costs nothing but the CLI heads it would have found.
const loginPathTimeout = 3 * time.Second

// Adopt merges the PATH a login shell would have into this process's, when the
// process was started without one.
//
// A program launched from Finder, the Dock or a .desktop file is started by the
// system launcher, which never reads a shell profile, so it gets
// PATH=/usr/bin:/bin:/usr/sbin:/sbin. Every CLI head lives outside that, and
// internal/provider/cli discovers heads with exec.LookPath, so the desktop app
// silently found none of them while the same hyctl in a terminal found them all
// (#689).
//
// Guarded on the PATH already being bare, because asking a shell costs about a
// second and a process started from a terminal already has the answer.
func Adopt() {
	if runtime.GOOS == "windows" {
		return // PATH comes from the registry, not a shell profile
	}
	if !looksBare(os.Getenv("PATH")) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginPathTimeout)
	defer cancel()
	if merged := mergePath(os.Getenv("PATH"), loginPath(ctx)); merged != "" {
		os.Setenv("PATH", merged)
	}
}

// systemDirs are what a launcher hands a process when nothing has set PATH.
// Anything outside this set means a shell profile has already been read.
var systemDirs = map[string]bool{
	"/usr/bin": true, "/bin": true, "/usr/sbin": true, "/sbin": true,
	"/usr/local/bin": true, "/usr/local/sbin": true,
}

// looksBare reports whether PATH holds nothing but system directories, which is
// the signature of a launcher start rather than a shell.
func looksBare(path string) bool {
	for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
		if dir != "" && !systemDirs[dir] {
			return false
		}
	}
	return true
}

// loginPath asks the user's shell what PATH it sets up, or returns "" if it
// cannot say. Interactive as well as login: a great many people set PATH in
// .zshrc, which a login shell alone does not read.
func loginPath(ctx context.Context) string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, sh, "-ilc", "printf %s \"$PATH\"").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// mergePath appends whatever the login shell knows that this process does not,
// preserving order and dropping duplicates.
//
// Appended rather than replaced: the running process's PATH may carry entries
// the shell has no idea about, and losing one to gain another is not a fix.
func mergePath(current, login string) string {
	if login == "" {
		return ""
	}
	seen := map[string]bool{}
	var out []string
	add := func(list string) {
		for _, dir := range strings.Split(list, string(os.PathListSeparator)) {
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	add(current)
	add(login)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, string(os.PathListSeparator))
}
