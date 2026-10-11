package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

type imagePreparer struct {
	servicePreparer
	calls   atomic.Int32
	started chan struct{}
	finish  chan struct{}
}

func (d *imagePreparer) CheckServiceImages(context.Context, model.Project) ([]runtime.ImageStatus, error) {
	return []runtime.ImageStatus{{Service: "web", Image: "ghcr.io/example/app:latest", Status: "available"}}, nil
}
func (d *imagePreparer) PrepareServiceImage(ctx context.Context, _ model.Project, _ string) (string, error) {
	d.calls.Add(1)
	close(d.started)
	select {
	case <-d.finish:
		return "sha256:" + strings.Repeat("2", 64), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestBackgroundImagePreparationDeduplicatesAndBindsDeploymentReview(t *testing.T) {
	a, d, _, send := apiFixture(t)
	a.Engine.Config.Keys[0].Projects = []string{"*"}
	a.Engine.Config.Keys[0].Scopes = append(a.Engine.Config.Keys[0].Scopes, "compose.admin")
	a.Auth, _ = auth.New(a.Engine.Config)
	prep := &imagePreparer{servicePreparer: servicePreparer{bgPreparer: bgPreparer{d, a.Engine.Config}, network: "network"}, started: make(chan struct{}), finish: make(chan struct{})}
	a.Engine.Docker = prep
	p := model.Project{ID: "demo", AppID: "demo", Mode: "compose", Environment: model.Production, State: "running", Active: "blue", BluePort: 3001, RouteService: "web", RoutePort: 80, Domains: []string{"app.example.com"}}
	runtime.Compose(a.Engine.Config, p)
	rel, err := prep.PrepareNative(context.Background(), p, "services: {}", "")
	if err != nil {
		t.Fatal(err)
	}
	rel.ID = "rel-original"
	p.Slots = map[string]model.Release{"blue": rel}
	p.Releases = []model.Release{rel}
	original := model.Job{ID: "original", ProjectID: p.ID, Status: "succeeded", Created: time.Now(), Input: model.Input{Project: &p}}
	if err = a.Engine.Store.Accept(original, &p, "original", "original", 10, 10); err != nil {
		t.Fatal(err)
	}
	pull := `{"expected_release_id":"rel-original","service":"web"}`
	if w := send("POST", "/v1/projects/demo/image-prepare", pull, "deploy.read", "denied"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := send("POST", "/v1/projects/demo/image-prepare", `{"expected_release_id":"stale","service":"web"}`, "deploy.execute", "stale"); w.Code != 409 {
		t.Fatal(w.Code)
	}
	key := imageTaskKey(p, "web")
	a.images.tasks[key] = imageTask{Status: "failed", Error: "IMAGE_PULL_FAILED", expires: time.Now().Add(time.Minute)}
	if w := send("POST", "/v1/projects/demo/image-prepare", pull, "deploy.execute", "failure-status"); w.Code != 200 || !strings.Contains(w.Body.String(), "IMAGE_PULL_FAILED") || prep.calls.Load() != 0 {
		t.Fatal("poll must expose a failed pull without silently restarting it", w.Code, w.Body.String())
	}
	if w := send("POST", "/v1/projects/demo/image-prepare", `{"expected_release_id":"rel-original","service":"web","retry":true}`, "deploy.execute", "retry"); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	for i := 0; i < 2; i++ {
		w := send("POST", "/v1/projects/demo/image-prepare", pull, "deploy.execute", "prepare"+string(rune('a'+i)))
		if w.Code != 202 && w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	select {
	case <-prep.started:
	case <-time.After(time.Second):
		t.Fatal("pull never started")
	}
	if busy, _ := a.Engine.Store.Busy(p.ID); busy {
		t.Fatal("background pull locked the project")
	}
	if prep.calls.Load() != 1 {
		t.Fatal("duplicate background pull")
	}
	input := serviceUpdateRequest{ExpectedRelease: rel.ID, Service: "web", Mode: "seamless", ContainerPort: 80}
	raw, _ := json.Marshal(input)
	scope := "deploy.execute projects.write sites.write"
	w := send("POST", "/v1/projects/demo/service-update-preview", string(raw), scope, "before")
	var before struct {
		Hash string `json:"review_sha256"`
	}
	json.Unmarshal(w.Body.Bytes(), &before)
	close(prep.finish)
	deadline := time.Now().Add(time.Second)
	for a.preparedImage(p, "web") == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if a.preparedImage(p, "web") == "" {
		t.Fatal("prepared digest lost")
	}
	input.Review = before.Hash
	raw, _ = json.Marshal(input)
	w = send("POST", "/v1/projects/demo/service-update", string(raw), scope, "old-review")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "SERVICE_REVIEW_CHANGED") {
		t.Fatal("review did not bind prepared image", w.Code, w.Body.String())
	}
	input.Review = ""
	raw, _ = json.Marshal(input)
	w = send("POST", "/v1/projects/demo/service-update-preview", string(raw), scope, "ready-review")
	var ready struct {
		Hash  string `json:"review_sha256"`
		Image string `json:"prepared_image"`
	}
	json.Unmarshal(w.Body.Bytes(), &ready)
	if w.Code != 200 || ready.Image == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	input.Review = ready.Hash
	raw, _ = json.Marshal(input)
	w = send("POST", "/v1/projects/demo/service-update", string(raw), scope, "apply")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job model.Job
	json.Unmarshal(w.Body.Bytes(), &job)
	stored, _ := a.Engine.Store.Job(job.ID)
	if stored.Input.ServiceUpdate.PreparedImage != ready.Image {
		t.Fatal("job repulls the mutable tag")
	}
	a.images.mu.Lock()
	task := a.images.tasks[key]
	task.expires = time.Now().Add(-time.Second)
	a.images.tasks[key] = task
	a.images.mu.Unlock()
	if a.preparedImage(p, "web") != "" {
		t.Fatal("expired preparation was reused")
	}
	// A configuration change must never inherit the old preparation.
	p.Releases[0].ID = "changed"
	if a.preparedImage(p, "web") != "" {
		t.Fatal("prepared image crossed project snapshots")
	}
}
