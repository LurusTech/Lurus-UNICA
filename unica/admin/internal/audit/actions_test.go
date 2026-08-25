package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The audit action vocabulary is a closed CHECK constraint in the database
// (migration 014, extended by 020). A handler that writes an unlisted verb
// compiles, passes its own tests against a fake logger, and then has every one
// of its entries refused at insert — the failure surfaces as a dropped audit
// row in production, which is exactly where an audit trail is least able to
// report its own absence.
//
// These tests read the vocabulary out of the migrations rather than restating
// it, so adding a verb in Go without adding the migration turns this red.

// TestAuditActionsAreInTheMigrationVocabulary walks the admin module for the
// action literals handlers pass to LogEvent and requires each to be one the
// database will accept.
func TestAuditActionsAreInTheMigrationVocabulary(t *testing.T) {
	allowed := migrationActionVocabulary(t)

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	used := logEventActionLiterals(t, root)
	if len(used) == 0 {
		t.Fatal("found no LogEvent action literals; the scan is not looking where the handlers are")
	}

	for _, u := range used {
		if !allowed[u.action] {
			t.Errorf("%s writes audit action %q, which the audit_logs_action_check "+
				"constraint does not allow (allowed: %s) — the insert is refused at "+
				"runtime and the entry is lost; add the verb in a migration or use a listed one",
				u.where, u.action, strings.Join(sortedKeys(allowed), ", "))
		}
	}
}

// TestAuditMiddlewareActionsAreInTheMigrationVocabulary covers the verbs the
// middleware derives from the HTTP method, which no LogEvent literal names.
func TestAuditMiddlewareActionsAreInTheMigrationVocabulary(t *testing.T) {
	allowed := migrationActionVocabulary(t)

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		action := methodToAction(method)
		if action == "" {
			continue
		}
		if !allowed[action] {
			t.Errorf("methodToAction(%s) = %q, which the audit_logs_action_check "+
				"constraint does not allow (allowed: %s)",
				method, action, strings.Join(sortedKeys(allowed), ", "))
		}
	}
}

type actionUse struct {
	action string
	where  string
}

// logEventActionLiterals collects the third argument of every LogEvent call
// that passes a string literal. Calls that forward a variable are not checked
// here — their values are decided elsewhere, and a scan that guessed at them
// would report on strings that no handler ever writes.
func logEventActionLiterals(t *testing.T, root string) []actionUse {
	t.Helper()

	var found []actionUse
	fset := token.NewFileSet()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "vendor" || strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // a file this test cannot parse is not this test's subject
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "LogEvent" || len(call.Args) < 3 {
				return true
			}
			lit, ok := call.Args[2].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = path
			}
			found = append(found, actionUse{
				action: value,
				where:  filepath.ToSlash(rel) + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line),
			})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

var actionCheckIn = regexp.MustCompile(`(?is)audit_logs_action_check\s+CHECK\s*\(\s*action\s+IN\s*\(([^)]*)\)`)

// migrationActionVocabulary reads the constraint the database will actually
// enforce: the last migration, by number, that defines audit_logs_action_check.
func migrationActionVocabulary(t *testing.T) map[string]bool {
	t.Helper()

	dir := filepath.Join("..", "..", "..", "router", "migrations")
	entries, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no migrations found under %s", dir)
	}
	sort.Strings(entries)

	var vocabulary map[string]bool
	var source string
	for _, path := range entries {
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		m := actionCheckIn.FindSubmatch(body)
		if m == nil {
			continue
		}
		set := map[string]bool{}
		for _, raw := range strings.Split(string(m[1]), ",") {
			v := strings.Trim(strings.TrimSpace(raw), "'")
			if v != "" {
				set[v] = true
			}
		}
		vocabulary = set
		source = filepath.Base(path)
	}

	if len(vocabulary) == 0 {
		t.Fatal("no migration defines audit_logs_action_check; the vocabulary this test " +
			"reads has moved or been dropped")
	}
	t.Logf("audit action vocabulary from %s: %s", source, strings.Join(sortedKeys(vocabulary), ", "))
	return vocabulary
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
