package docs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func contract(t *testing.T) map[string]any {
	t.Helper()
	b, err := assets.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestOpenAPIReferencesAndSecurity(t *testing.T) {
	spec := contract(t)
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/") {
					t.Fatalf("external reference: %s", ref)
				}
				var target any = spec
				for _, part := range strings.Split(ref[2:], "/") {
					part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
					obj, ok := target.(map[string]any)
					if !ok || obj[part] == nil {
						t.Fatalf("broken reference: %s", ref)
					}
					target = obj[part]
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(spec)
	security := spec["security"].([]any)
	if len(security) != 1 || len(security[0].(map[string]any)) != 2 {
		t.Fatal("signed operations must require both mTLS and HMAC, not alternatives")
	}
	for _, name := range []string{"MutualTLS", "HMACSignature"} {
		if scopes, ok := security[0].(map[string]any)[name].([]any); !ok || len(scopes) != 0 {
			t.Fatalf("missing scheme or invalid OAuth scopes: %s", name)
		}
	}
	ids := map[string]bool{}
	operations := 0
	for path, item := range spec["paths"].(map[string]any) {
		for _, raw := range item.(map[string]any) {
			op := raw.(map[string]any)
			id := op["operationId"].(string)
			if id == "" || ids[id] {
				t.Fatalf("nonunique operation ID: %s", id)
			}
			ids[id] = true
			operations++
			if strings.HasPrefix(path, "/v1/") {
				if _, override := op["security"]; override {
					t.Fatalf("signed endpoint overrides global authentication: %s", path)
				}
				if len(op["x-required-scopes"].([]any)) == 0 {
					t.Fatalf("missing permissions: %s", path)
				}
				headers := map[string]bool{}
				for _, rawParam := range op["parameters"].([]any) {
					param := rawParam.(map[string]any)
					if ref, ok := param["$ref"].(string); ok {
						name := strings.TrimPrefix(ref, "#/components/parameters/")
						param = spec["components"].(map[string]any)["parameters"].(map[string]any)[name].(map[string]any)
					}
					if param["in"] == "header" && param["required"] == true {
						headers[param["name"].(string)] = true
					}
				}
				for _, name := range []string{"X-Deploy-Key-ID", "X-Deploy-Timestamp", "X-Deploy-Scopes", "Idempotency-Key", "X-Actor-ID", "X-Request-ID"} {
					if !headers[name] {
						t.Fatalf("missing signed header %s on %s", name, path)
					}
				}
			} else {
				security := op["security"].([]any)
				if len(security) != 1 || len(security[0].(map[string]any)) != 1 || security[0].(map[string]any)["MutualTLS"] == nil {
					t.Fatalf("unsigned resource must still require mTLS: %s", path)
				}
			}
		}
	}
	if operations != 25 {
		t.Fatalf("expected 19 API operations and 6 health/docs operations, got %d", operations)
	}
}

func TestResponseSchemasMatchModels(t *testing.T) {
	schemas := contract(t)["components"].(map[string]any)["schemas"].(map[string]any)
	for name, value := range map[string]any{"Project": model.Project{}, "Release": model.Release{}, "Job": model.Job{}, "ServiceInfo": model.ServiceInfo{}, "DomainInfo": model.DomainInfo{}, "Inventory": model.Inventory{}, "ExistingProject": model.ExistingProject{}, "ExistingService": model.ExistingService{}, "ExistingSite": model.ExistingSite{}} {
		schema := schemas[name].(map[string]any)
		properties := schema["properties"].(map[string]any)
		required := map[string]bool{}
		for _, field := range schema["required"].([]any) {
			required[field.(string)] = true
		}
		fields := 0
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			parts := strings.Split(typ.Field(i).Tag.Get("json"), ",")
			if parts[0] == "-" {
				continue
			}
			fields++
			if properties[parts[0]] == nil {
				t.Fatalf("%s schema omits %s", name, parts[0])
			}
			optional := len(parts) > 1 && parts[1] == "omitempty"
			if required[parts[0]] == optional {
				t.Fatalf("%s.%s required flag differs from JSON model", name, parts[0])
			}
		}
		if fields != len(properties) {
			t.Fatalf("%s schema has undocumented model differences", name)
		}
	}
}
