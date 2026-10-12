package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"go.yaml.in/yaml/v3"
)

type NativeEnvironmentPreparer interface {
	PrepareNative(context.Context, model.Project, string, string) (model.Release, error)
}

// Adoption baselines freeze resolved values for ownership checks, so their
// inline environment would override every newer saved .env. EnvironmentSource
// restores the original environment/env_file rules for each service that still
// carries the frozen baseline environment (compared by content, since the
// desktop editor reformats JSON as YAML). Services whose environment a later
// configuration deployment changed keep those rules, and every other setting
// comes from the current release; on-disk Compose edits are never imported.
func (d Docker) EnvironmentSource(ctx context.Context, p model.Project, current model.Release) (string, string, error) {
	source, dotenv, err := EditableConfiguration(d.Config, p, current)
	if err != nil {
		return source, dotenv, err
	}
	source, err = d.RestoreAdoptedEnvironment(ctx, p, source)
	if err != nil {
		return "", "", err
	}
	return source, dotenv, nil
}

// RestoreAdoptedEnvironment applies EnvironmentSource's rule recovery to any
// Compose source, such as a saved draft. Other sources are returned unchanged.
func (d Docker) RestoreAdoptedEnvironment(ctx context.Context, p model.Project, source string) (string, error) {
	if p.Adoption == nil || p.Adoption.EnvironmentRules || len(p.Releases) == 0 {
		return source, nil
	}
	baseline, err := ComposeMirror(d.Config, p, p.Releases[0])
	if err != nil {
		return "", err
	}
	var doc, frozen map[string]any
	if yaml.Unmarshal([]byte(source), &doc) != nil || yaml.Unmarshal(baseline, &frozen) != nil {
		return "", model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	stale := []string{}
	for name, raw := range object(doc["services"]) {
		svc, old := object(raw), object(object(frozen["services"])[name])
		if svc != nil && old != nil && old["environment"] != nil && reflect.DeepEqual(svc["environment"], old["environment"]) {
			stale = append(stale, name)
		}
	}
	if len(stale) == 0 {
		return source, nil
	}
	dir := d.workingDir(p)
	if err := adoptedSourceDirectory(d.Config, p); err != nil {
		return "", err
	}
	if len(p.Adoption.ConfigFiles) == 0 || len(p.Adoption.ConfigFiles) > 16 {
		return "", model.Fail("MIGRATION_SOURCE_UNSAFE")
	}
	args := []string{"compose", "--project-name", p.Adoption.ComposeProject, "--project-directory", dir}
	for _, file := range p.Adoption.ConfigFiles {
		if _, err := adoptionRead(dir, file, 64<<10); err != nil {
			return "", err
		}
		args = append(args, "--file", file)
	}
	// Keep interpolation references and literal-dollar escapes intact for the
	// normal resolution passes against the saved .env.
	args = append(args, "config", "--format", "json", "--no-interpolate", "--no-env-resolution")
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, dir, args)
	var original map[string]any
	if err != nil || result.Truncated || len(result.Output) > 64<<10 || json.Unmarshal(result.Output, &original) != nil {
		return "", model.Fail("COMPOSE_VALIDATION_FAILED")
	}
	for _, name := range stale {
		svc, rule := object(object(doc["services"])[name]), object(object(original["services"])[name])
		if rule == nil {
			return "", model.Fail("COMPOSE_CONFIG_DIVERGED")
		}
		for _, key := range []string{"environment", "env_file"} {
			if rule[key] == nil {
				delete(svc, key)
			} else {
				svc[key] = rule[key]
			}
		}
	}
	var out strings.Builder
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(doc) != nil || out.Len() > 64<<10 {
		return "", model.Fail("COMPOSE_LIMIT")
	}
	return out.String(), nil
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
	if resolver, ok := preparer.(interface {
		EnvironmentSource(context.Context, model.Project, model.Release) (string, string, error)
	}); ok {
		source, _, err = resolver.EnvironmentSource(ctx, p, current)
		if err != nil {
			return current, err
		}
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
	if err = secure.Atomic(composeRevisionPath(c, p, current.Compose), b, 0600); err != nil {
		return current, err
	}
	if err = secure.Atomic(filepath.Join(projectDir(c, p), "source", current.Compose+".yml"), []byte(source), 0600); err != nil {
		return current, err
	}
	return current, nil
}

// PrepareServiceEnvironment applies the latest saved .env to just the reviewed
// service. Draft Compose edits still require a configuration deployment.
func (d Docker) PrepareServiceEnvironment(ctx context.Context, p model.Project, plan *model.ServiceUpdate) error {
	current, _ := p.Current()
	source, dotenv, err := d.EnvironmentSource(ctx, p, current)
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
	if err = secure.Atomic(composeRevisionPath(d.Config, p, release.Compose), b, 0600); err != nil {
		return err
	}
	plan.SourceRelease = &release
	return nil
}
