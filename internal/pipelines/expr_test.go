// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func testEnv() *ExprEnv {
	e := NewExprEnv()
	e.Contexts["github"] = map[string]any{"ref": "refs/heads/main", "event_name": "push", "run_number": float64(7), "event": map[string]any{"number": float64(3)}}
	e.Contexts["matrix"] = map[string]any{"os": "ubuntu-latest", "go": "1.25", "experimental": true}
	e.Contexts["env"] = map[string]any{"FOO": "bar", "EMPTY": ""}
	e.Contexts["secrets"] = map[string]any{"TOKEN": "s3cret"}
	e.Contexts["steps"] = map[string]any{"build": map[string]any{"outputs": map[string]any{"version": "1.2.3"}, "outcome": "success"}}
	e.Contexts["needs"] = map[string]any{"build": map[string]any{"result": "success", "outputs": map[string]any{"list": []any{"a", "b"}}}}
	return e
}

func TestExprEval(t *testing.T) {
	e := testEnv()
	cases := []struct {
		src  string
		want any
	}{
		{"github.ref == 'refs/heads/main'", true},
		{"github.ref == 'REFS/HEADS/MAIN'", true}, // case-insensitive like Actions
		{"github.ref != 'refs/heads/main'", false},
		{"github.event_name == 'push' && matrix.os == 'ubuntu-latest'", true},
		{"github.event_name == 'pull_request' || matrix.go == '1.25'", true},
		{"!matrix.experimental", false},
		{"matrix.experimental", true},
		{"env.FOO", "bar"},
		{"env.EMPTY || 'default'", "default"},
		{"github.run_number > 5", true},
		{"github.run_number >= 7 && github.run_number < 8", true},
		{"github['ref']", "refs/heads/main"},
		{"github.event.number", float64(3)},
		{"steps.build.outputs.version", "1.2.3"},
		{"needs.build.result == 'success'", true},
		{"needs.build.outputs.list[1]", "b"},
		{"contains(github.ref, 'HEADS')", true},
		{"contains(needs.build.outputs.list, 'a')", true},
		{"contains(needs.build.outputs.list, 'z')", false},
		{"startsWith(github.ref, 'refs/heads/')", true},
		{"endsWith(github.ref, '/main')", true},
		{"format('{0}-{1}', matrix.os, matrix.go)", "ubuntu-latest-1.25"},
		{"format('{{literal}} {0}', 1)", "{literal} 1"},
		{"join(needs.build.outputs.list, '+')", "a+b"},
		{"join(needs.build.outputs.list)", "a,b"},
		{"fromJSON('{\"a\": [1, 2]}').a[1]", float64(2)},
		{"toJSON(matrix.go)", "\"1.25\""},
		{"success()", true},
		{"failure()", false},
		{"always()", true},
		{"cancelled()", false},
		{"null == ''", true}, // both coerce to 0, as on GitHub
		{"null == 0", true},
		{"'1' == 1", true},
		{"true == 'true'", false},
		{"github.missing == null", true},
		{"github.missing.deeper", nil},
		{"0x10 == 16", true},
		{"(1 < 2) && (2 <= 2) && (3 > 2) && (3 >= 3)", true},
		{"'abc' < 'abd'", true},
		{"-1 < 0", true},
		{"'it''s'", "it's"},
	}
	for _, c := range cases {
		got, err := e.Eval(c.src)
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.src, got, c.want)
		}
	}
}

func TestExprErrors(t *testing.T) {
	e := testEnv()
	for _, src := range []string{"github.ref = 'x'", "unknownfn(1)", "\"double\"", "(1", "steps.*.outputs", "format('{0}')", "fromJSON('{')"} {
		if _, err := e.Eval(src); err == nil {
			t.Errorf("%s: expected an error", src)
		}
	}
}

func TestInterpolate(t *testing.T) {
	e := testEnv()
	got, err := e.Interpolate("go-${{ matrix.go }}-${{ format('{0}', github.run_number) }}-${{ secrets.TOKEN }}-${{ env.EMPTY }}.")
	if err != nil || got != "go-1.25-7-s3cret-." {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, _ := e.Interpolate("no expressions"); got != "no expressions" {
		t.Fatal(got)
	}
	got, err = e.Interpolate("${{ format('a}}b', 1) }}") // }} inside a string literal
	if err != nil || got != "a}b" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := e.Interpolate("${{ unterminated"); err == nil {
		t.Fatal("expected an error")
	}
	partial, err := e.InterpolateWith("${{ matrix.os }}/${{ github.ref }}", func(expr string) bool {
		a, _ := Analyze(expr)
		return len(a.Contexts) == 1 && a.Contexts[0] == "matrix"
	})
	if err != nil || partial != "ubuntu-latest/${{ github.ref }}" {
		t.Fatalf("partial: %q %v", partial, err)
	}
	if StripExpr("${{ github.ref == 'x' }}") != "github.ref == 'x'" || StripExpr("github.ref") != "github.ref" {
		t.Fatal("StripExpr")
	}
}

func TestAnalyze(t *testing.T) {
	a, err := Analyze("github.ref == 'x' && hashFiles('**/go.sum') != '' && secrets.TOKEN && needs.build.outputs.v && matrix['os']")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Contexts, []string{"github", "matrix", "needs", "secrets"}) {
		t.Fatalf("contexts %v", a.Contexts)
	}
	if !reflect.DeepEqual(a.Functions, []string{"hashfiles"}) {
		t.Fatalf("functions %v", a.Functions)
	}
	want := map[string]bool{"secrets.TOKEN": true, "needs.build.outputs.v": true, "needs.build": true, "matrix.os": true, "github.ref": true}
	for _, r := range a.Refs {
		delete(want, r)
	}
	if len(want) != 0 {
		t.Fatalf("missing refs %v in %v", want, a.Refs)
	}
	a, _ = Analyze("steps.*.outputs.x")
	if !a.HasFilter {
		t.Fatal("expected HasFilter")
	}
}

func TestGlobAndHashFiles(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"main", "main", true}, {"main", "maintenance", false}, {"release/*", "release/1.x", true}, {"release/*", "release/1/2", false},
		{"release/**", "release/1/2", true}, {"**/go.sum", "go.sum", true}, {"**/go.sum", "a/b/go.sum", true}, {"*.go", "a/b.go", false},
		{"**/*.go", "a/b.go", true}, {"src/**/*.ts", "src/x/y.ts", true}, {"src/**/*.ts", "lib/x.ts", false}, {"feature**", "feature-x", true},
		{"refs/heads/main", "refs/heads/main", true}, {"dist/", "dist", true}, {"docs/**", "docs/a.md", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.name); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v", c.pat, c.name, got)
		}
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.sum"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "b", "go.sum"), []byte("y"), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "b", "main.go"), []byte("z"), 0o644)
	os.WriteFile(filepath.Join(dir, ".git", "go.sum"), []byte("ignored"), 0o644)
	files, err := GlobFiles(dir, []string{"**/go.sum"})
	if err != nil || !reflect.DeepEqual(files, []string{"a/b/go.sum", "go.sum"}) {
		t.Fatalf("files %v %v", files, err)
	}
	files, _ = GlobFiles(dir, []string{"a", "!**/*.go"})
	if !reflect.DeepEqual(files, []string{"a/b/go.sum"}) {
		t.Fatalf("dir pattern with exclusion: %v", files)
	}
	h1, err := HashFiles(dir, "**/go.sum")
	if err != nil || len(h1) != 64 {
		t.Fatalf("hash %q %v", h1, err)
	}
	os.WriteFile(filepath.Join(dir, "go.sum"), []byte("changed"), 0o644)
	if h2, _ := HashFiles(dir, "**/go.sum"); h2 == h1 {
		t.Fatal("hash did not change")
	}
	if h, _ := HashFiles(dir, "nothing/*"); h != "" {
		t.Fatal("empty match must hash to ''")
	}
}
