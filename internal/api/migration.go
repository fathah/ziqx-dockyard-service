package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/engine"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

// MigrationAssessment contains only fixed status codes and safe inventory
// metadata. Neither Compose source nor environment values leave the server.
type MigrationAssessment struct {
	ProjectID          string           `json:"project_id"`
	Status             string           `json:"status"`
	SourceSHA256       string           `json:"source_sha256,omitempty"`
	Checks             []MigrationCheck `json:"checks"`
	ExecutionAvailable bool             `json:"execution_available"`
}

type MigrationCheck struct {
	Code   string `json:"code"`
	Status string `json:"status"`
}

func migrationComposeName(name string) bool {
	switch name {
	case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	}
	return false
}

func migrationAssessment(e *engine.Engine, id string) (MigrationAssessment, bool, error) {
	result := MigrationAssessment{ProjectID: id, Status: "blocked", Checks: []MigrationCheck{}}
	add := func(code string, pass bool) {
		status := "blocked"
		if pass {
			status = "passed"
		}
		result.Checks = append(result.Checks, MigrationCheck{Code: code, Status: status})
	}
	v, err := e.Store.Inventory()
	if err != nil {
		return result, false, err
	}
	var p *model.ExistingProject
	for i := range v.Projects {
		if v.Projects[i].ID == id && !v.Projects[i].Managed {
			p = &v.Projects[i]
			break
		}
	}
	if p == nil {
		return result, false, nil
	}
	sourceFailed := false
	for _, warning := range v.Warnings {
		if warning != "PROJECT_METADATA_INCOMPLETE" {
			sourceFailed = true
		}
	}
	fresh := v.ProjectsObservedAt != nil && v.CaddyObservedAt != nil && v.DockerObservedAt != nil &&
		time.Since(*v.ProjectsObservedAt) < 10*time.Minute &&
		time.Since(*v.CaddyObservedAt) < 10*time.Minute &&
		time.Since(*v.DockerObservedAt) < 10*time.Minute && !sourceFailed
	add("INVENTORY_FRESH", fresh)
	add("SOURCE_PRESENT", p.Present)
	add("SOURCE_METADATA_COMPLETE", len(p.Warnings) == 0)
	add("SINGLE_COMPOSE_FILE", len(p.ComposeFiles) == 1 && migrationComposeName(p.ComposeFiles[0]))
	add("PROJECT_ID_SUPPORTED", config.ID.MatchString(p.Name))
	manualRoutes := 0
	for _, site := range v.Sites {
		for _, projectID := range site.ProjectIDs {
			if projectID == id {
				manualRoutes++
			}
		}
	}
	add("NO_MANUAL_CADDY_CUTOVER", manualRoutes == 0)
	ports := 0
	for _, service := range p.Services {
		ports += len(service.PublishedPorts)
	}
	add("NO_EXISTING_PUBLISHED_PORTS", ports == 0)
	// The current desktop installer deliberately provisions inventory-only
	// credentials and a non-deployment placeholder template. Do not suggest
	// eligibility when the host has no approved application template.
	approved := false
	for id := range e.Config.Templates {
		if id != "inventory-only" {
			approved = true
		}
	}
	add("APPROVED_TEMPLATES_CONFIGURED", approved)
	if p.Present && len(p.ComposeFiles) == 1 && migrationComposeName(p.ComposeFiles[0]) && config.ID.MatchString(p.Name) {
		path := filepath.Join(e.Config.ProjectsRoot, p.Name, p.ComposeFiles[0])
		st, statErr := os.Lstat(path)
		trusted := statErr == nil && st.Mode().IsRegular() && st.Size() <= 64<<10 && secure.Check(path, false) == nil
		add("SOURCE_FILE_TRUSTED", trusted)
		if trusted {
			f, openErr := os.Open(path)
			if openErr == nil {
				opened, statErr := f.Stat()
				if statErr != nil || !os.SameFile(st, opened) {
					f.Close()
					add("SOURCE_FILE_READABLE", false)
					return result, true, nil
				}
				b, readErr := io.ReadAll(io.LimitReader(f, (64<<10)+1))
				f.Close()
				if readErr == nil && len(b) <= 64<<10 {
					hash := sha256.Sum256(b)
					result.SourceSHA256 = hex.EncodeToString(hash[:])
					appImage := ""
					for _, service := range p.Services {
						if service.Name == "app" {
							appImage = service.Image
						}
					}
					matches := []string{}
					for id, t := range e.Config.Templates {
						if id != "inventory-only" && strings.HasPrefix(appImage, t.ImageRepository+"@sha256:") {
							matches = append(matches, id)
						}
					}
					add("APP_TEMPLATE_UNAMBIGUOUS", len(matches) == 1)
					if len(matches) == 1 {
						candidate := model.Project{ID: p.Name, Template: matches[0], Environment: model.Production}
						plan, parseErr := runtime.ParseCompose(e.Config, candidate, string(b))
						add("COMPOSE_POLICY_COMPATIBLE", parseErr == nil)
						if parseErr == nil {
							add("STATELESS_STACK", len(plan.Volumes) == 0)
						}
					}
				} else {
					add("SOURCE_FILE_READABLE", false)
				}
			} else {
				add("SOURCE_FILE_READABLE", false)
			}
		}
	}
	result.Status = "candidate"
	for _, check := range result.Checks {
		if check.Status != "passed" {
			result.Status = "blocked"
			break
		}
	}
	// A safe preflight is necessary but insufficient for takeover. The durable
	// parallel deployment and traffic-cutover job is not implemented yet.
	result.ExecutionAvailable = false
	return result, true, nil
}
