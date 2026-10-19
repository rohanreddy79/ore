// SPDX-License-Identifier: AGPL-3.0-only

package sshd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{"git-upload-pack 'acme/app.git'", []string{"git-upload-pack", "acme/app.git"}, false},
		{"git-receive-pack '/acme/app.git'", []string{"git-receive-pack", "/acme/app.git"}, false},
		{`git-upload-pack "acme/app.git"`, []string{"git-upload-pack", "acme/app.git"}, false},
		{`git upload-pack acme/app.git`, []string{"git", "upload-pack", "acme/app.git"}, false},
		{`a "b \"c\" d" 'e f' g\ h`, []string{"a", `b "c" d`, "e f", "g h"}, false},
		{"  spaced   out  ", []string{"spaced", "out"}, false},
		{"", nil, false},
		{"unterminated 'quote", nil, true},
		{`unterminated "quote`, nil, true},
		{"it''s", []string{"its"}, false},
	}
	for _, c := range cases {
		got, err := SplitCommand(c.in)
		if (err != nil) != c.err {
			t.Errorf("%q: err %v", c.in, err)
			continue
		}
		if !c.err && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseRepoPath(t *testing.T) {
	good := map[string][2]string{
		"acme/app.git":  {"acme", "app"},
		"/acme/app.git": {"acme", "app"},
		"acme/app":      {"acme", "app"},
		"a-b_c.d/x.y":   {"a-b_c.d", "x.y"},
	}
	for in, want := range good {
		org, name, ok := ParseRepoPath(in)
		if !ok || org != want[0] || name != want[1] {
			t.Errorf("%q: got %q %q %v", in, org, name, ok)
		}
	}
	for _, in := range []string{"", "app.git", "acme/", "/app", "acme/app/extra.git", "../etc/passwd", "acme/../x", "Acme/App.git", "acme/.hidden", "a b/c"} {
		if _, _, ok := ParseRepoPath(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestHostKeyIsGeneratedOnceAndReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "ssh_host_ed25519_key")
	a := &Server{HostKeyPath: path}
	ln, err := a.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("host key file: %v %v", info, err)
	}
	if _, err := os.Stat(path + ".pub"); err != nil {
		t.Fatal(err)
	}
	b := &Server{HostKeyPath: path}
	ln, err = b.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if a.HostKeyFingerprint() == "" || a.HostKeyFingerprint() != b.HostKeyFingerprint() {
		t.Fatalf("fingerprints differ: %s vs %s", a.HostKeyFingerprint(), b.HostKeyFingerprint())
	}
	os.WriteFile(path, []byte("garbage"), 0o600)
	if _, err := (&Server{HostKeyPath: path}).Listen("127.0.0.1:0"); err == nil {
		t.Fatal("corrupt host key accepted")
	}
}
