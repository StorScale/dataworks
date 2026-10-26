package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	platform "github.com/StorScale/dataworks"
)

// A stack is the platform's Compose project, in a directory on disk.
type stack struct{ dir string }

func version() string { return platform.Version() }

// openStack uses dir, or else the stack built into this binary, written out
// once per version of its contents.
func openStack(dir string) (*stack, error) {
	if dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err != nil {
			return nil, fmt.Errorf("no compose.yaml in %s", dir)
		}
		abs, err := filepath.Abs(dir)
		return &stack{abs}, err
	}
	sum, err := digest(platform.Stack)
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(cache, "storscale", "stack-"+version()+"-"+sum[:12])
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err == nil {
		return &stack{dir}, nil
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	err = fs.WalkDir(platform.Stack, "stack", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, strings.TrimPrefix(path, "stack"))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := platform.Stack.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
	}
	return &stack{dir}, nil
}

// digest is a hash of every file's path and contents.
func digest(fsys fs.FS) (string, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// files are the stack's Compose files. A checkout's stack also gets the
// checkout's compose.dev.yaml, which builds the platform's own images from source.
func (s *stack) files() []string {
	files := []string{filepath.Join(s.dir, "compose.yaml")}
	if dev := filepath.Join(s.dir, "..", "compose.dev.yaml"); fileExists(dev) {
		files = append(files, dev)
	}
	return files
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// compose runs docker compose on the stack, attached to this terminal.
func (s *stack) compose(args ...string) error {
	base := []string{"compose", "--project-directory", s.dir}
	for _, f := range s.files() {
		base = append(base, "--file", f)
	}
	cmd := exec.Command("docker", append(base, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return err
		}
		return fmt.Errorf("docker compose: %w (is Docker, with Compose 2.24 or later, installed?)", err)
	}
	return nil
}

// setting is a variable as Compose sees it: the environment, then the stack's .env.
func (s *stack) setting(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	f, err := os.Open(filepath.Join(s.dir, ".env"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && k == name {
			return v
		}
	}
	return ""
}
