package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

type NativeEnvironmentPreparer interface {
	PrepareNative(context.Context, model.Project, string, string) (model.Release, error)
}

// PrepareRestartEnvironment refreshes every service's environment from the saved
// .env while retaining deployed Compose settings. Draft Compose edits are not
// applied by Start/Restart. Rollback deliberately keeps its historical inputs.
func PrepareRestartEnvironment(ctx context.Context, c config.Config, preparer NativeEnvironmentPreparer, p model.Project, current model.Release) (model.Release, error) {
	_, dotenv, saved := ReadDraft(c, p)
	if !saved {
		return current, nil
	}
	source, _, err := EditableConfiguration(c, p, current)
	if err != nil {
		return current, err
	}
	resolved, err := preparer.PrepareNative(ctx, p, source, dotenv)
	if err != nil {
		return current, err
	}
	_, oldBytes, err := nativeRelease(c, p, current)
	if err != nil {
		return current, err
	}
	_, newBytes, err := nativeRelease(c, p, resolved)
	if err != nil {
		return current, err
	}
	var old, next map[string]any
	if json.Unmarshal(oldBytes, &old) != nil || json.Unmarshal(newBytes, &next) != nil {
		return current, model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	for name, raw := range object(old["services"]) {
		svc := object(raw)
		candidate := object(object(next["services"])[name])
		if svc == nil || candidate == nil {
			return current, model.Fail("COMPOSE_CONFIG_DIVERGED")
		}
		if candidate["environment"] == nil {
			delete(svc, "environment")
		} else {
			svc["environment"] = candidate["environment"]
		}
	}
	old["x-dockyard-env-sha256"] = next["x-dockyard-env-sha256"]
	b, err := json.MarshalIndent(old, "", "  ")
	if err != nil || len(b) > 2<<20 {
		return current, model.Fail("COMPOSE_LIMIT")
	}
	sum := sha256.Sum256(b)
	current.Compose = "cmp-" + hex.EncodeToString(sum[:])
	current.Environment = resolved.Environment
	if err = secure.Atomic(composeRevisionPath(c, p.ID, current.Compose), b, 0600); err != nil {
		return current, err
	}
	if err = secure.Atomic(filepath.Join(projectDir(c, p.ID), "source", current.Compose+".yml"), []byte(source), 0600); err != nil {
		return current, err
	}
	return current, nil
}

// PrepareServiceEnvironment applies the latest saved .env to just the reviewed
// service. Draft Compose edits still require a configuration deployment.
func (d Docker) PrepareServiceEnvironment(ctx context.Context, p model.Project, plan *model.ServiceUpdate) error {
	current, _ := p.Current()
	source, dotenv, err := EditableConfiguration(d.Config, p, current)
	if err != nil {
		return err
	}
	if _, saved, ok := ReadDraft(d.Config, p); ok {
		dotenv = saved
	}
	release, err := d.PrepareNative(ctx, p, source, dotenv)
	if err != nil {
		return err
	}
	_, resolved, err := nativeRelease(d.Config, p, release)
	if err != nil {
		return err
	}
	var next map[string]any
	if json.Unmarshal(resolved, &next) != nil {
		return model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	// Preserve the active instance's ports, networks, command and other settings.
	// Only its resolved container environment changes in an individual update.
	base, name := plan.Previous.Release, plan.Previous.Name
	_, active, err := nativeRelease(d.Config, p, base)
	if err != nil {
		return err
	}
	var doc map[string]any
	if json.Unmarshal(active, &doc) != nil {
		return model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	svc := object(object(doc["services"])[name])
	candidate := object(object(next["services"])[plan.Service])
	if svc == nil || candidate == nil {
		return model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	if reflect.DeepEqual(svc["environment"], candidate["environment"]) {
		return nil
	}
	if candidate["environment"] == nil {
		delete(svc, "environment")
	} else {
		svc["environment"] = candidate["environment"]
	}
	// Active overrides pin their image. Continue pulling the deployed tag.
	_, configured, err := nativeRelease(d.Config, p, current)
	if err != nil {
		return err
	}
	var original map[string]any
	if json.Unmarshal(configured, &original) != nil {
		return model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	svc["image"] = object(object(original["services"])[plan.Service])["image"]
	if name != plan.Service {
		if plan.Mode == "restart" {
			for key, raw := range object(doc["networks"]) {
				network, _ := object(raw)["name"].(string)
				if aliases := plan.Previous.NetworkAliases[network]; len(aliases) > 0 {
					object(svc["networks"])[key] = map[string]any{"aliases": aliases}
				}
			}
		}
		delete(object(doc["services"]), name)
		object(doc["services"])[plan.Service] = svc
	}
	doc["x-dockyard-env-sha256"] = next["x-dockyard-env-sha256"]
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil || len(b) > 2<<20 {
		return model.Fail("COMPOSE_LIMIT")
	}
	sum := sha256.Sum256(b)
	release.Compose = "cmp-" + hex.EncodeToString(sum[:])
	if err = secure.Atomic(composeRevisionPath(d.Config, p.ID, release.Compose), b, 0600); err != nil {
		return err
	}
	plan.SourceRelease = &release
	return nil
}
