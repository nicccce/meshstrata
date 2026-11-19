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
	for _, root := range []map[string]any{open, ws, parse(t, ControlV2Schema), parse(t, BundleSchema), parse(t, OfflineBundleV2Schema)} {
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
	implemented := map[string]bool{
		"get /api/v1/system":           true,
		"post /api/v1/auth/initialize": true, "post /api/v1/auth/login": true, "get /api/v1/auth/session": true, "post /api/v1/auth/logout": true, "post /api/v1/auth/password": true,
		"get /api/v1/nodes": true, "post /api/v1/nodes": true, "get /api/v1/nodes/{node_id}": true, "patch /api/v1/nodes/{node_id}": true,
		"get /api/v1/nodes/{node_id}/enrollment": true, "post /api/v1/nodes/{node_id}/enrollment": true, "post /api/v1/nodes/{node_id}/remove": true, "post /api/v1/nodes/{node_id}/certificates/revoke": true,
		"get /api/v1/tasks": true, "get /api/v1/tasks/{task_id}": true,
		"post /api/v1/tasks/{task_id}/cancel": true, "post /api/v1/tasks/{task_id}/retry": true, "post /api/v1/tasks/{task_id}/rollback": true,
		"get /control/v1/tasks/{task_id}/bundle": true,
		"post /enroll/v1":                        true, "post /enroll/v1/renew": true, "get /control/v1": true,
	}
	// Reserved business writes must explicitly require both administrator session and CSRF.
	for path, item := range open["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			op := raw.(map[string]any)
			if (op["x-implemented"] == true) != implemented[method+" "+path] {
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

func TestV2SessionAndObservationContract(t *testing.T) {
	root := parse(t, ControlV2Schema)
	if root["x-production-enabled"] != false || root["title"] != control.SubprotocolV2 {
		t.Fatal("production V2 gate changed")
	}
	formats := root["x-runtime-bundle-formats"].([]any)
	if len(formats) != 2 || formats[0] != float64(1) || formats[1] != float64(2) {
		t.Fatal("dual format contract missing")
	}
	models := map[string]any{"ObservationRequestV2": control.ObservationRequestV2{}, "ObservationReportV2": control.ObservationReportV2{}, "InventoryV2": control.InventoryV2{}, "EgressObservationV2": control.EgressObservationV2{}, "InterfaceObservationV2": control.InterfaceObservationV2{}, "PinnedSocketSpecV2": control.PinnedSocketSpecV2{}, "ProtocolCapabilityV2": control.ProtocolCapabilityV2{}, "SocketSpec": control.SocketSpec{}}
	for name, model := range models {
		spec := schema(t, root, "$defs", name)
		props := spec["properties"].(map[string]any)
		want := fields(reflect.TypeOf(model))
		if len(props) != len(want) || spec["additionalProperties"] != false {
			t.Fatal("observation schema shape", name)
		}
		for field := range want {
			if props[field] == nil {
				t.Fatal("missing observation field", name, field)
			}
		}
	}
	for _, envelope := range root["oneOf"].([]any) {
		spec := envelope.(map[string]any)
		props := spec["properties"].(map[string]any)
		if props["session_id"] == nil || props["version"].(map[string]any)["const"] != float64(2) || spec["additionalProperties"] != false {
			t.Fatal("unbound V2 envelope")
		}
	}
}

// Protect the existing Copy URL product flow against a header-only regression.
func TestSubscriptionCapabilityLinkContract(t *testing.T) {
	open := parse(t, OpenAPI)
	op := schema(t, open, "paths", "/subscriptions/v1/config/{token}", "get")
	if op["x-implemented"] != false || len(op["security"].([]any)) != 0 {
		t.Fatal("Copy URL must remain reserved and require no cookie/header security")
	}
	auth := op["x-authentication"].(map[string]any)
	if auth["in"] != "path" || auth["parameter"] != "token" || auth["scope"] != "single_subscription_download" || auth["administrator_authority"] != false {
		t.Fatal("missing download-only path capability")
	}
	params := op["parameters"].([]any)
	if len(params) != 1 {
		t.Fatal("link import must not require extra parameters/headers")
	}
	param := params[0].(map[string]any)
	if param["name"] != "token" || param["in"] != "path" || param["required"] != true {
		t.Fatal("missing normal OpenAPI token path parameter")
	}
	for _, raw := range schema(t, open, "components", "securitySchemes") {
		if raw.(map[string]any)["in"] == "path" {
			t.Fatal("OpenAPI apiKey cannot use in:path")
		}
	}
	link := schema(t, open, "paths", "/api/v1/subscriptions/{subscription_id}/download-link", "post")
	security := link["security"].([]any)[0].(map[string]any)
	if security["AdminSession"] == nil || security["CSRF"] == nil || link["x-implemented"] != false {
		t.Fatal("explicit link retrieval must require admin+CSRF and remain reserved")
	}
	recovery := link["x-token-recovery"].(map[string]any)
	if recovery["reset_required_after_refresh"] != false || recovery["verification"] != "hash" || recovery["migration"] != "010" || recovery["credential_purpose"] != "subscription_download" || recovery["subscription_field"] != "download_token_credential_ref" || recovery["credential_table"] != "subscription_download_credentials" {
		t.Fatal("Copy URL after refresh requires recoverable current token, not forced reset")
	}
	for _, operation := range []map[string]any{op, link, schema(t, open, "paths", "/subscriptions/v1/config", "get")} {
		policy := operation["x-sensitive-link"].(map[string]any)
		if policy["minimum_entropy_bits"] != float64(256) || policy["redirects"] != false || policy["ordinary_dto_contains_token"] != false {
			t.Fatal("sensitive-link policy weakened")
		}
		for _, raw := range operation["responses"].(map[string]any) {
			h := raw.(map[string]any)["headers"].(map[string]any)
			if h["Cache-Control"].(map[string]any)["schema"].(map[string]any)["const"] != "no-store" || h["Referrer-Policy"].(map[string]any)["schema"].(map[string]any)["const"] != "no-referrer" {
				t.Fatal("sensitive response may cache or leak referrer")
			}
		}
	}
}

func TestI01SecureAuthContract(t *testing.T) {
	open := parse(t, OpenAPI)
	init := schema(t, open, "paths", "/api/v1/auth/initialize", "post")
	found := false
	for _, raw := range init["parameters"].([]any) {
		p := raw.(map[string]any)
		if p["name"] == "X-Meshstrata-Bootstrap" && p["required"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("trusted bootstrap header missing")
	}
	session := schema(t, open, "components", "schemas", "AuthSession", "properties")
	if len(session) != 3 || session["csrf_token"] == nil || session["username"] == nil || session["expires_at"] == nil {
		t.Fatal("unsafe session DTO")
	}
	security := schema(t, open, "paths", "/api/v1/auth/password", "post")["security"].([]any)[0].(map[string]any)
	if security["AdminSession"] == nil || security["CSRF"] == nil {
		t.Fatal("password lacks auth/CSRF")
	}
}

func TestPrivateBundleSchemaFields(t *testing.T) {
	root := parse(t, BundleSchema)
	models := map[string]any{"Bundle": control.Bundle{}, "Pin": control.Pin{}, "RuleInput": control.RuleInput{}, "DNSInput": control.DNSInput{}, "EgressInput": control.EgressInput{}, "Resource": control.Resource{}, "LocalMaterial": control.LocalMaterial{}, "PrivateCredential": control.PrivateCredential{}, "BundleAuthorization": control.BundleAuthorization{}, "BundleListener": control.BundleListener{}, "Tombstone": control.Tombstone{}}
	for name, value := range models {
		spec := root
		if name != "Bundle" {
			spec = schema(t, root, "$defs", name)
		}
		got := spec["properties"].(map[string]any)
		want := fields(reflect.TypeOf(value))
		if len(got) != len(want) || spec["additionalProperties"] != false {
			t.Fatalf("%s fields/unknown policy mismatch", name)
		}
		for k := range want {
			if got[k] == nil {
				t.Fatalf("%s.%s missing", name, k)
			}
		}
		if len(spec["required"].([]any)) != len(want) {
			t.Fatalf("%s required mismatch", name)
		}
	}
	ws := parse(t, ControlSchema)
	if len(schema(t, ws, "$defs", "Restriction", "properties")) != len(fields(reflect.TypeOf(control.Restriction{}))) {
		t.Fatal("restriction schema mismatch")
	}
	if int(root["x-max-bytes"].(float64)) != control.MaxBundleBytes {
		t.Fatal("bundle length mismatch")
	}
}

func TestOfflineBundleV2Contract(t *testing.T) {
	root := parse(t, OfflineBundleV2Schema)
	wire := parse(t, ControlSchema)["x-runtime-bundle-formats"].([]any)
	if len(wire) != 1 || wire[0] != float64(control.BundleVersion) {
		t.Fatal("wire still advertises only the active format")
	}
	if root["x-runtime-supported"] != false || schema(t, root, "properties", "format")["const"] != float64(control.BundleVersion2) ||
		control.BundleVersion != 1 || root["x-max-bytes"] != float64(control.MaxBundleBytes) {
		t.Fatal("offline codec must not advertise runtime support")
	}
	models := map[string]any{
		"BundleV2": control.BundleV2{}, "ListenerV2": control.ListenerV2{}, "TerminalV2": control.TerminalV2{},
		"SocketV2": control.SocketV2{}, "NodeRuleV2": control.NodeRuleV2{}, "NextHop": control.NextHop{},
		"InternalRetirement": control.InternalRetirement{}, "RouteTail": control.RouteTail{}, "HopSpec": control.HopSpec{},
		"ProxyEndpoint": control.ProxyEndpoint{}, "EgressSpec": control.EgressSpec{}, "EgressAuth": control.EgressAuth{},
		"PinnedEgressTarget": control.PinnedEgressTarget{}, "EgressTarget": control.EgressTarget{}, "EgressRule": control.EgressRule{},
	}
	for name, value := range models {
		spec := root
		if name != "BundleV2" {
			spec = schema(t, root, "$defs", name)
		}
		props := spec["properties"].(map[string]any)
		want := fields(reflect.TypeOf(value))
		if len(want) != len(props) || spec["additionalProperties"] != false {
			t.Fatalf("%s shape mismatch", name)
		}
		required := map[string]bool{}
		for _, name := range spec["required"].([]any) {
			required[name.(string)] = true
		}
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if required[name] == strings.Contains(tag, ",omitempty") {
				t.Fatalf("%s.%s optionality mismatch", name, tag)
			}
		}
		for field := range want {
			if props[field] == nil {
				t.Fatalf("%s.%s missing", name, field)
			}
		}
	}
}
