package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
)

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
