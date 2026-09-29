package proc

import (
	"context"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// procFS lists processes from a /proc-shaped file system. It is compiled everywhere so it can be tested
// with fixtures; only the Linux System() uses it on the real /proc.
type procFS struct{ root fs.FS }

func (l procFS) List(ctx context.Context) ([]Process, error) {
	ents, err := fs.ReadDir(l.root, ".")
	if err != nil {
		return nil, err
	}
	var out []Process
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		stat, err := fs.ReadFile(l.root, path.Join(e.Name(), "stat"))
		if err != nil {
			continue // exited meanwhile or unreadable
		}
		comm, ppid, ok := parseStat(string(stat))
		if !ok {
			continue
		}
		p := Process{PID: pid, PPID: ppid, Name: normalize(comm)}
		if exe, err := readLink(l.root, path.Join(e.Name(), "exe")); err == nil && exe != "" {
			p.Path = exe
			// comm is cut at 15 bytes; the base name of the executable is authoritative.
			p.Name = normalize(path.Base(exe))
		}
		out = append(out, p)
	}
	return out, nil
}

// readLink resolves a symlink when the file system supports it.
func readLink(fsys fs.FS, name string) (string, error) {
	if rl, ok := fsys.(fs.ReadLinkFS); ok {
		return rl.ReadLink(name)
	}
	return "", fs.ErrInvalid
}

// parseStat extracts comm and ppid from /proc/<pid>/stat. comm sits in parentheses and may itself contain
// spaces and parentheses, so the last ')' ends it.
func parseStat(s string) (comm string, ppid int, ok bool) {
	open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || closing < open {
		return "", 0, false
	}
	comm = s[open+1 : closing]
	rest := strings.Fields(s[closing+1:]) // state ppid pgrp ...
	if len(rest) < 2 {
		return "", 0, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return "", 0, false
	}
	return comm, ppid, true
}
