package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/engine"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

func namedEnvFixture(t *testing.T) (*API, model.Project, func(string, string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Auth, _ = auth.New(a.Engine.Config)
	prep := nativePreparer{d, a.Engine.Config}
	a.Engine.Docker = prep
	p := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Production, State: "running", Active: "blue"}
	if err := runtime.Compose(a.Engine.Config, p); err != nil {
		t.Fatal(err)
	}
	rel, err := prep.PrepareNative(context.Background(), p, "services: {}", "MAIN=main-private\n", map[string]string{".env.backend": "TOKEN=named-private\n"})
	if err != nil {
		t.Fatal(err)
	}
	rel.ID = "rel-original"
	p.Slots, p.Releases = map[string]model.Release{"blue": rel}, []model.Release{rel}
	j := model.Job{ID: "original", ProjectID: p.ID, Status: "succeeded", Created: time.Now()}
	if err := a.Engine.Store.Accept(j, &p, "original", "original", 10, 10); err != nil {
		t.Fatal(err)
	}
	return a, p, send
}

func TestNamedEnvConfigurationReadIsPrivateAndDiscoversFiles(t *testing.T) {
	a, p, send := namedEnvFixture(t)
	if err := os.WriteFile(filepath.Join(a.Engine.Config.ProjectsRoot, p.ID, ".env.frontend"), []byte("PUBLIC_URL=https://example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, scopes := range []string{"deploy.read", "deploy.execute"} {
		w := send("GET", "/v1/projects/demo/configuration?env_files=1", "", scopes, fmt.Sprintf("private-%d", i))
		if w.Code != 403 || strings.Contains(w.Body.String(), "named-private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := send("GET", "/v1/projects/demo/configuration", "", "deploy.environment", "read-default")
	var defaults map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &defaults) != nil || defaults["env_files_supported"] != true || defaults["env_file"] != "MAIN=main-private\n" || defaults["env_files"] != nil || strings.Contains(w.Body.String(), "named-private") || !reflect.DeepEqual(defaults["env_file_names"], []any{".env.backend", ".env.frontend"}) {
		t.Fatal("default response must preserve main while withholding named contents", w.Code, w.Body.String())
	}
	w = send("GET", "/v1/projects/demo/configuration?env_files=1", "", "deploy.environment", "read-named")
	var got struct {
		EnvFiles map[string]string `json:"env_files"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.EnvFiles[".env.backend"] != "TOKEN=named-private\n" || got.EnvFiles[".env.frontend"] != "PUBLIC_URL=https://example.com\n" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	for i, path := range []string{"/v1/projects", "/v1/projects/demo", "/v1/projects/demo/releases", "/v1/jobs/original"} {
		w := send("GET", path, "", "deploy.read", fmt.Sprintf("metadata-%d", i))
		if w.Code != 200 || strings.Contains(w.Body.String(), "named-private") || strings.Contains(w.Body.String(), "main-private") {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}

func TestNamedEnvConfigurationQueryRequiresExplicitOne(t *testing.T) {
	_, _, send := namedEnvFixture(t)
	for i, query := range []string{"env_files=", "env_files=0", "env_files=true", "env_files=1&env_files=1", "files=1"} {
		w := send("GET", "/v1/projects/demo/configuration?"+query, "", "deploy.environment", fmt.Sprintf("query-%d", i))
		if w.Code != 400 || strings.Contains(w.Body.String(), "named-private") {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
}

func TestNamedEnvConfigurationReadsOneCompleteDraft(t *testing.T) {
	a, p, send := namedEnvFixture(t)
	save := func(marker string) error {
		env := "TOKEN=" + marker + "\n"
		_, err := runtime.SaveDraft(a.Engine.Config, p, marker, env, map[string]string{".env.backend": env})
		return err
	}
	if err := save("marker-initial"); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if err := save(fmt.Sprintf("marker-%d", i)); err != nil {
				done <- err
				return
			}
		}
	}()
	defer func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 20; i++ {
		w := send("GET", "/v1/projects/demo/configuration?env_files=1", "", "deploy.environment", fmt.Sprintf("atomic-read-%d", i))
		var result struct {
			Compose string            `json:"compose_yaml"`
			Main    string            `json:"env_file"`
			Files   map[string]string `json:"env_files"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Main != "TOKEN="+result.Compose+"\n" || result.Files[".env.backend"] != result.Main {
			t.Fatal("configuration mixed separate saves", w.Code, w.Body.String())
		}
	}
}

func TestNamedEnvDraftOmittedPreservesAndEmptyMapClears(t *testing.T) {
	a, p, send := namedEnvFixture(t)
	files := map[string]string{".env.frontend": "URL=frontend-private\n", ".env.backend": "TOKEN=backend-private\n"}
	body, _ := json.Marshal(map[string]any{"compose_yaml": "services: draft", "env_files": files})
	w := send("POST", "/v1/projects/demo/draft", string(body), "deploy.environment", "save-named")
	if w.Code != 200 || strings.Contains(w.Body.String(), "backend-private") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = send("POST", "/v1/projects/demo/draft", `{"compose_yaml":"services: newer"}`, "deploy.environment", "preserve-named")
	saved, err := runtime.ReadDraftEnvFiles(a.Engine.Config, p)
	_, main, _ := runtime.ReadDraft(a.Engine.Config, p)
	if w.Code != 200 || err != nil || !reflect.DeepEqual(saved, files) || main != "MAIN=main-private\n" {
		t.Fatal(w.Code, w.Body.String(), saved, main, err)
	}
	w = send("POST", "/v1/projects/demo/draft", `{"compose_yaml":"services: cleared","env_files":{}}`, "deploy.environment", "clear-named")
	saved, err = runtime.ReadDraftEnvFiles(a.Engine.Config, p)
	if w.Code != 200 || err != nil || len(saved) != 0 {
		t.Fatal(w.Code, w.Body.String(), saved, err)
	}
}

func TestDraftSaveWaitsForAdmissionBeforeReadingPreservedEnvironment(t *testing.T) {
	a, p, send := namedEnvFixture(t)
	a.Engine.Admission.Lock()
	locked := true
	defer func() {
		if locked {
			a.Engine.Admission.Unlock()
		}
	}()
	started := make(chan struct{})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		close(started)
		result <- send("POST", "/v1/projects/demo/draft", `{"compose_yaml":"services: submitted"}`, "deploy.environment", "save-contended")
	}()
	<-started
	select {
	case w := <-result:
		t.Fatal("draft save bypassed admission", w.Code, w.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	if _, _, ok := runtime.ReadDraft(a.Engine.Config, p); ok {
		t.Fatal("draft changed while admission was held")
	}
	latest := map[string]string{".env.backend": "TOKEN=latest-private\n"}
	if _, err := runtime.SaveDraft(a.Engine.Config, p, "services: latest", "MAIN=latest\n", latest); err != nil {
		t.Fatal(err)
	}
	a.Engine.Admission.Unlock()
	locked = false
	select {
	case w := <-result:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("draft save did not resume after admission")
	}
	compose, main, files, ok, err := runtime.ReadDraftConfiguration(a.Engine.Config, p)
	if err != nil || !ok || compose != "services: submitted" || main != "MAIN=latest\n" || !reflect.DeepEqual(files, latest) {
		t.Fatal("save preserved environment read before admission", compose, main, files, ok, err)
	}
}

func TestDraftSaveRemainsAvailableWhileProjectJobIsRunning(t *testing.T) {
	a, p, send := namedEnvFixture(t)
	j := model.Job{ID: "running-job", ProjectID: p.ID, RequestID: "request-running-job", Status: "running", Action: "deploy", Created: time.Now()}
	if err := a.Engine.Store.Accept(j, nil, "running-job", "running-job", 10, 10); err != nil {
		t.Fatal(err)
	}
	w := send("POST", "/v1/projects/demo/draft", `{"compose_yaml":"services: next"}`, "deploy.environment", "save-during-job")
	if w.Code != 200 {
		t.Fatal("active job prevented draft save", w.Code, w.Body.String())
	}
}

func TestNamedEnvDeploySnapshotsOmittedOrExplicitEmpty(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprint(clear), func(t *testing.T) {
			a, p, send := namedEnvFixture(t)
			files := map[string]string{".env.backend": "TOKEN=draft-private\n"}
			if _, err := runtime.SaveDraft(a.Engine.Config, p, "services: draft", "MAIN=saved\n", files); err != nil {
				t.Fatal(err)
			}
			// A mirror must not silently reintroduce files removed by an explicit map.
			if err := os.WriteFile(filepath.Join(a.Engine.Config.ProjectsRoot, p.ID, ".env.backend"), []byte("TOKEN=stale-host\n"), 0600); err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"environment": "production", "compose_yaml": "services: {}", "expected_release_id": "rel-original"}
			if clear {
				payload["env_files"] = map[string]string{}
			}
			body, _ := json.Marshal(payload)
			w := send("POST", "/v1/projects/demo/deploy", string(body), "deploy.environment deploy.execute", "deploy-named")
			var accepted struct {
				ID string `json:"job_id"`
			}
			if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &accepted) != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			job, err := a.Engine.Store.Job(accepted.ID)
			if err != nil || job.Input.Release == nil {
				t.Fatal(err, job)
			}
			saved, err := runtime.ReadReleaseEnvFiles(a.Engine.Config, p, *job.Input.Release)
			if err != nil || clear && len(saved) != 0 || !clear && !reflect.DeepEqual(saved, files) {
				t.Fatal(saved, err)
			}
			if strings.Contains(w.Body.String(), "draft-private") {
				t.Fatal("secret in accepted job")
			}
		})
	}
}

func TestNamedEnvInvalidDraftRejectedWithoutChangingSavedFiles(t *testing.T) {
	a, p, send := namedEnvFixture(t)
	cases := []map[string]string{{".env": "TOKEN=x"}, {".env../outside": "TOKEN=x"}, {".env.frontend": strings.Repeat("x", 64<<10)}}
	tooMany := map[string]string{}
	for i := 0; i < 17; i++ {
		tooMany[fmt.Sprintf(".env.service%d", i)] = ""
	}
	cases = append(cases, tooMany)
	for i, files := range cases {
		body, _ := json.Marshal(map[string]any{"compose_yaml": "services: invalid", "env_files": files})
		w := send("POST", "/v1/projects/demo/draft", string(body), "deploy.environment", fmt.Sprintf("invalid-%d", i))
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
		if _, _, ok := runtime.ReadDraft(a.Engine.Config, p); ok {
			t.Fatal("invalid file payload replaced draft")
		}
	}
}

func TestNamedEnvPayloadRejectedForTemplateProject(t *testing.T) {
	a, _, _, send := apiFixture(t)
	p := model.Project{ID: "demo", AppID: "demo", Environment: model.Production, State: "awaiting_release", Template: "node", TemplateRevision: engine.TemplateHash(a.Engine.Config.Templates["node"])}
	j := model.Job{ID: "original", ProjectID: p.ID, Status: "succeeded", Created: time.Now()}
	if err := a.Engine.Store.Accept(j, &p, "original", "original", 10, 10); err != nil {
		t.Fatal(err)
	}
	w := send("POST", "/v1/projects/demo/deploy", `{"environment":"production","compose_yaml":"services: {}","env_files":{}}`, "deploy.environment deploy.execute", "template-named")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "LEGACY_PROJECT_REQUIRES_VARIABLES") {
		t.Fatal(w.Code, w.Body.String())
	}
}
