package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func parse(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func schema(t *testing.T, root map[string]any, path ...string) map[string]any {
	t.Helper()
	var v any = root
	for _, k := range path {
		v = v.(map[string]any)[k]
	}
	return v.(map[string]any)
}
func fields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			for k := range fields(f.Type) {
				out[k] = true
			}
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out[tag] = true
		}
	}
	return out
}
func TestContractReferencesAndGoFields(t *testing.T) {
	open := parse(t, OpenAPI)
	ws := parse(t, ControlSchema)
	for _, root := range []map[string]any{open, ws} {
		var walk func(any)
		walk = func(v any) {
			switch n := v.(type) {
			case map[string]any:
				for k, x := range n {
					if k == "$ref" {
						path := strings.TrimPrefix(x.(string), "#/")
						var target any = root
						for _, p := range strings.Split(path, "/") {
							m, ok := target.(map[string]any)
							if !ok || m[p] == nil {
								t.Fatalf("unresolved reference %s", path)
							}
							target = m[p]
						}
					} else {
						walk(x)
					}
				}
			case []any:
				for _, x := range n {
					walk(x)
				}
			}
		}
		walk(root)
	}
	for name, value := range map[string]any{"Node": domain.Node{}, "Task": domain.Task{}, "NodeResult": domain.NodeResult{}, "Mutation": domain.Mutation{}, "Problem": domain.Problem{}, "FieldError": domain.FieldError{}, "TaskAccepted": domain.TaskAccepted{}, "RevisionRef": domain.RevisionRef{}} {
		got := schema(t, open, "components", "schemas", name, "properties")
		want := fields(reflect.TypeOf(value))
		if len(got) != len(want) {
			t.Fatalf("%s field count: schema %d Go %d", name, len(got), len(want))
		}
		for k := range want {
			if got[k] == nil {
				t.Fatalf("%s.%s missing", name, k)
			}
		}
	}
	for name, value := range map[string]any{"Command": control.Command{}, "Result": control.Result{}, "Receipt": control.Receipt{}, "Hello": control.Hello{}} {
		got := schema(t, ws, "$defs", name, "properties")
		want := fields(reflect.TypeOf(value))
		if len(got) != len(want) {
			t.Fatalf("%s field count differs", name)
		}
		for k := range want {
			if got[k] == nil {
				t.Fatalf("%s.%s missing", name, k)
			}
		}
	}
	// Reserved business writes must explicitly require both administrator session and CSRF.
	for path, item := range open["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			op := raw.(map[string]any)
			if op["x-implemented"] == true && path != "/api/v1/system" {
				t.Fatalf("unexpected implemented endpoint %s", path)
			}
			if strings.HasPrefix(path, "/api/v1/") && !strings.HasPrefix(path, "/api/v1/auth/") && method != "get" {
				security := op["security"].([]any)[0].(map[string]any)
				if security["AdminSession"] == nil || security["CSRF"] == nil {
					t.Fatalf("write lacks auth/CSRF: %s", path)
				}
			}
		}
	}
}
