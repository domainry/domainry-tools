// Architectural boundaries follow the existing Identity/Agent module conventions.
package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestLayeredLayoutAndPrivateImplementations(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, rel := range []string{"internal/application/tool", "internal/adapter/recordtools", "internal/adapter/calendartools", "internal/adapter/mailtools", "internal/adapter/webtools", "internal/adapter/reporttools", "internal/adapter/accounttools", "module"} {
		if info, err := os.Stat(filepath.Join(root, rel)); err != nil || !info.IsDir() {
			t.Errorf("missing boundary %s: %v", rel, err)
		}
	}
	public := map[string]bool{}
	for _, p := range []string{"module", "calculation", "timeutil"} {
		public[p] = true
	}
	filename := regexp.MustCompile(`^[a-z][a-z0-9_]*\.go$`)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "dist", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if !filename.MatchString(entry.Name()) {
			t.Errorf("nonstandard Go filename: %s", rel)
		}
		top := strings.Split(rel, "/")[0]
		if top != "internal" && top != "cmd" && !public[top] {
			t.Errorf("implementation outside its private layer: %s", rel)
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, e := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if e != nil {
			return e
		}
		if top == "module" {
			if rel != "module/module.go" {
				t.Errorf("module must be a single thin facade: %s", rel)
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
					if fn.Recv != nil || len(fn.Body.List) != 1 {
						t.Errorf("facade contains implementation: %s", fn.Name)
					}
				}
			}
		}
		for _, im := range f.Imports {
			imported, _ := strconv.Unquote(im.Path.Value)
			if strings.HasPrefix(rel, "internal/adapter/accounttools/") && (strings.Contains(imported, "/calendartools") || strings.Contains(imported, "/mailtools") || strings.Contains(imported, "/webtools") || strings.HasPrefix(imported, "github.com/domainry/domainry-connector-sdk/")) {
				t.Errorf("shared account mechanism imports a business protocol: %s -> %s", rel, imported)
			}
			if strings.HasPrefix(rel, "internal/adapter/reporttools/") && (strings.HasPrefix(imported, "github.com/domainry/domainry-agent-sdk") || strings.Contains(imported, "/accounttools")) {
				t.Errorf("report adapter must consume Report/neutral tool contracts: %s -> %s", rel, imported)
			}
			for _, family := range []string{"calendartools", "mailtools", "webtools", "reporttools"} {
				if !strings.HasPrefix(rel, "internal/adapter/"+family+"/") {
					continue
				}
				for _, peer := range []string{"calendartools", "mailtools", "webtools", "reporttools"} {
					if peer != family && strings.Contains(imported, "/"+peer) {
						t.Errorf("business tool adapters depend on each other: %s -> %s", rel, imported)
					}
				}
			}
			if strings.HasPrefix(rel, "internal/adapter/") && strings.HasPrefix(imported, "github.com/domainry/") && !strings.HasPrefix(imported, "github.com/domainry/domainry-tools/") {
				name := strings.Split(strings.TrimPrefix(imported, "github.com/domainry/"), "/")[0]
				if !strings.HasSuffix(name, "-sdk") && !strings.HasSuffix(imported, "/contract") {
					t.Errorf("tool adapter imports external implementation: %s -> %s", rel, imported)
				}
			}
			domain := strings.HasPrefix(rel, "internal/domain/")
			application := strings.HasPrefix(rel, "internal/application/")
			contract := top == "contract"
			if domain || application || contract {
				if imported == "database/sql" || imported == "net/http" {
					t.Errorf("policy/contract imports concrete I/O: %s -> %s", rel, imported)
				}
				if strings.HasPrefix(imported, "github.com/domainry/domainry-tools/") {
					local := strings.TrimPrefix(imported, "github.com/domainry/domainry-tools/")
					for _, adapter := range []string{"internal/infrastructure", "internal/transport", "internal/assembly", "internal/adapter", "module"} {
						if local == adapter || strings.HasPrefix(local, adapter+"/") {
							t.Errorf("policy depends on implementation: %s -> %s", rel, imported)
						}
					}
					if domain && strings.HasPrefix(local, "internal/application/") {
						t.Errorf("domain imports application: %s", rel)
					}
				} else if strings.HasPrefix(imported, "github.com/domainry/") {
					name := strings.Split(strings.TrimPrefix(imported, "github.com/domainry/"), "/")[0]
					if !strings.HasSuffix(name, "-sdk") && !strings.HasSuffix(imported, "/contract") {
						t.Errorf("policy imports external implementation: %s -> %s", rel, imported)
					}
				}
			}
			if strings.Contains(imported, "/internal/") && !strings.HasPrefix(imported, "github.com/domainry/domainry-tools/") {
				t.Errorf("cross-module internal import: %s", imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
