package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/auth"
	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

type imageRuntime interface {
	CheckServiceImages(context.Context, model.Project) ([]runtime.ImageStatus, error)
	PrepareServiceImage(context.Context, model.Project, string) (string, error)
}
type imageTask struct {
	Status   string                `json:"status"`
	Image    string                `json:"image,omitempty"`
	Services []runtime.ImageStatus `json:"services,omitempty"`
	Error    string                `json:"error,omitempty"`
	expires  time.Time
}
type imageTasks struct {
	mu    sync.Mutex
	tasks map[string]imageTask
	slots chan struct{}
}

func imageTaskKey(p model.Project, service string) string {
	b, _ := json.Marshal([]any{p, service})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (a *API) preparedImage(p model.Project, service string) string {
	a.images.mu.Lock()
	defer a.images.mu.Unlock()
	task := a.images.tasks[imageTaskKey(p, service)]
	if task.Status == "ready" && time.Now().Before(task.expires) {
		return task.Image
	}
	return ""
}

// Work outlives the HTTP request, but is bounded and deduplicated. Only Docker's
// image cache changes; durable deployment jobs remain the sole container writers.
func (a *API) serviceImages(w http.ResponseWriter, r *http.Request, principal auth.Principal, body []byte, id string, prepare bool) {
	var input struct {
		ExpectedRelease string `json:"expected_release_id"`
		Service         string `json:"service,omitempty"`
		Retry           bool   `json:"retry,omitempty"`
	}
	if !config.ID.MatchString(id) || secure.Decode(body, &input) != nil || input.ExpectedRelease == "" || prepare && !config.ServiceName.MatchString(input.Service) || !prepare && (input.Service != "" || input.Retry) {
		problem(w, 400, "REQUEST_INVALID", principal.RequestID)
		return
	}
	scope := "deploy.read"
	if prepare {
		scope = "deploy.execute"
	}
	if !require(w, principal, scope, id) {
		return
	}
	if prepare && !a.nativeAuthority(w, principal) {
		return
	}
	e := a.Engine
	e.Admission.Lock()
	defer e.Admission.Unlock()
	if !e.Ready() {
		problem(w, 503, "RECOVERY_REQUIRED", principal.RequestID)
		return
	}
	p, err := e.Store.Project(id)
	if err != nil {
		problem(w, 404, "PROJECT_NOT_FOUND", principal.RequestID)
		return
	}
	current, ok := p.Current()
	if !ok || current.ID != input.ExpectedRelease {
		problem(w, 409, "CONFIGURATION_CHANGED", principal.RequestID)
		return
	}
	if !p.NativeCompose() || p.State != "running" {
		problem(w, 409, "SERVICE_UPDATE_UNAVAILABLE", principal.RequestID)
		return
	}
	if err := p.ValidateTarget(); err != nil {
		fail(w, err, principal.RequestID)
		return
	}
	d, ok := e.Docker.(imageRuntime)
	if !ok {
		problem(w, 409, "SERVICE_UPDATE_UNAVAILABLE", principal.RequestID)
		return
	}
	if prepare {
		busy, err := e.Store.Busy(id)
		if err != nil {
			fail(w, err, principal.RequestID)
			return
		}
		if busy {
			problem(w, 409, "PROJECT_BUSY", principal.RequestID)
			return
		}
	}
	key := imageTaskKey(p, input.Service)
	a.images.mu.Lock()
	now := time.Now()
	for k, t := range a.images.tasks {
		if (input.Retry && k == key && t.Status == "failed") || now.After(t.expires) && t.Status != "pulling" && t.Status != "checking" {
			delete(a.images.tasks, k)
		}
	}
	if task, ok := a.images.tasks[key]; ok {
		a.images.mu.Unlock()
		write(w, 200, task)
		return
	}
	if len(a.images.tasks) >= 64 {
		a.images.mu.Unlock()
		problem(w, 429, "RATE_LIMITED", principal.RequestID)
		return
	}
	select {
	case a.images.slots <- struct{}{}:
	default:
		a.images.mu.Unlock()
		problem(w, 429, "RATE_LIMITED", principal.RequestID)
		return
	}
	task := imageTask{Status: "checking", expires: now.Add(2 * time.Minute)}
	if prepare {
		task.Status = "pulling"
		task.expires = now.Add(15 * time.Minute)
	}
	a.images.tasks[key] = task
	a.images.mu.Unlock()
	go func() {
		defer func() { <-a.images.slots }()
		timeout := 2 * time.Minute
		if prepare {
			timeout = 10 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		result := imageTask{Status: "ready", expires: time.Now().Add(time.Minute)}
		var err error
		if prepare {
			result.Image, err = d.PrepareServiceImage(ctx, p, input.Service)
			result.expires = time.Now().Add(10 * time.Minute)
		} else {
			result.Services, err = d.CheckServiceImages(ctx, p)
			result.expires = time.Now().Add(time.Minute)
		}
		if err != nil {
			result.Status = "failed"
			result.Error = faultCode(err)
			result.expires = time.Now().Add(10 * time.Second)
		}
		a.images.mu.Lock()
		a.images.tasks[key] = result
		a.images.mu.Unlock()
	}()
	write(w, 202, task)
}
