package runtime

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

type ImageStatus struct {
	Service      string `json:"service"`
	Image        string `json:"image"`
	Status       string `json:"status"`
	CurrentImage string `json:"current_image,omitempty"`
	LatestImage  string `json:"latest_image,omitempty"`
	Error        string `json:"error,omitempty"`
}

func imageDigest(s string) bool {
	return len(s) == 71 && strings.HasPrefix(s, "sha256:") && strings.Trim(s[7:], "0123456789abcdef") == ""
}

func (d Docker) serviceImages(p model.Project) (model.Release, map[string]string, error) {
	r, ok := p.Current()
	if !ok || !p.NativeCompose() {
		return r, nil, model.Fail("SERVICE_UPDATE_UNAVAILABLE")
	}
	_, b, err := nativeRelease(d.Config, p, r)
	if err != nil {
		return r, nil, err
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return r, nil, model.Fail("COMPOSE_CONFIG_DIVERGED")
	}
	images := map[string]string{}
	for name, raw := range object(doc["services"]) {
		if image, ok := object(raw)["image"].(string); ok && image != "" {
			images[name] = strings.ReplaceAll(image, "$$", "$")
		}
	}
	return r, images, nil
}

// Checks registry metadata only. Immutable digest references cannot acquire an update.
func (d Docker) CheckServiceImages(ctx context.Context, p model.Project) ([]ImageStatus, error) {
	r, refs, err := d.serviceImages(p)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for name, ref := range refs {
		if strings.HasPrefix(ref, "ghcr.io/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	statuses := make([]ImageStatus, 0, len(names))
	// Services using the same image/platform share a registry request.
	manifests := map[string][]byte{}
	for _, name := range names {
		status := ImageStatus{Service: name, Image: refs[name], Status: "unknown"}
		if strings.Contains(refs[name], "@sha256:") {
			status.Status = "pinned"
			statuses = append(statuses, status)
			continue
		}
		previous, ok := p.ServiceInstances[name]
		if !ok {
			previous = model.ServiceInstance{Name: name, Release: r}
		}
		c, e := d.inspectServiceInstance(ctx, p, previous)
		if e != nil || !c.State.Running {
			status.Error = "SERVICE_NOT_RUNNING"
			statuses = append(statuses, status)
			continue
		}
		status.CurrentImage = c.Image
		local, e := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p), []string{"image", "inspect", c.Image})
		var platform []struct {
			ID           string `json:"Id"`
			OS           string `json:"Os"`
			Architecture string
			Variant      string
		}
		if e != nil || local.Truncated || json.Unmarshal(local.Output, &platform) != nil || len(platform) != 1 || platform[0].ID != c.Image || platform[0].OS == "" || platform[0].Architecture == "" {
			status.Error = "IMAGE_IDENTITY_UNKNOWN"
			statuses = append(statuses, status)
			continue
		}
		raw, found := manifests[refs[name]]
		if !found {
			result, e := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p), []string{"manifest", "inspect", "--verbose", refs[name]})
			if e != nil || result.Truncated {
				status.Error = "REGISTRY_CHECK_FAILED"
				statuses = append(statuses, status)
				continue
			}
			raw = result.Output
			manifests[refs[name]] = raw
		}
		latest, e := registryImageID(raw, platform[0].OS, platform[0].Architecture, platform[0].Variant)
		if e != nil {
			status.Error = "REGISTRY_CHECK_FAILED"
		} else {
			status.LatestImage = latest
			status.Status = "current"
			if latest != c.Image {
				status.Status = "available"
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// Docker's verbose output may be a single manifest or a multi-platform array.
// Compare config digests (image IDs), never manifest-list digests with image IDs.
func registryImageID(raw []byte, os, arch, variant string) (string, error) {
	type manifest struct {
		Descriptor struct {
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		}
		SchemaV2Manifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		OCIManifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
	}
	var list []manifest
	if json.Unmarshal(raw, &list) != nil {
		var single manifest
		if json.Unmarshal(raw, &single) != nil {
			return "", model.Fail("REGISTRY_CHECK_FAILED")
		}
		list = []manifest{single}
	}
	matches := []string{}
	for _, m := range list {
		p := m.Descriptor.Platform
		// arm64's implicit v8 is equivalent to Docker image inspect's empty variant.
		v := p.Variant
		if arch == "arm64" && v == "v8" && variant == "" {
			v = ""
		}
		if p.OS != os || p.Architecture != arch || v != variant {
			continue
		}
		id := m.SchemaV2Manifest.Config.Digest
		if id == "" {
			id = m.OCIManifest.Config.Digest
		}
		if imageDigest(id) {
			matches = append(matches, id)
		}
	}
	if len(matches) != 1 {
		return "", model.Fail("REGISTRY_CHECK_FAILED")
	}
	return matches[0], nil
}

// Downloads only the selected service. No containers or routes are changed.
func (d Docker) PrepareServiceImage(ctx context.Context, p model.Project, service string) (string, error) {
	if !config.ServiceName.MatchString(service) || p.State != "running" || p.ZeroDowntime {
		return "", model.Fail("SERVICE_UPDATE_UNAVAILABLE")
	}
	r, refs, err := d.serviceImages(p)
	if err != nil {
		return "", err
	}
	image, ok := refs[service]
	if !ok {
		return "", model.Fail("SERVICE_IMAGE_REQUIRED")
	}
	if _, err = d.serviceCommand(ctx, p, model.ServiceInstance{Name: service, Release: r}, "pull", service); err != nil {
		return "", model.Fail("IMAGE_PULL_FAILED")
	}
	result, err := d.Runner.Run(ctx, d.Config.DockerBinary, projectDir(d.Config, p), []string{"image", "inspect", image})
	var images []struct {
		ID string `json:"Id"`
	}
	if err != nil || result.Truncated || json.Unmarshal(result.Output, &images) != nil || len(images) != 1 || !imageDigest(images[0].ID) {
		return "", model.Fail("IMAGE_IDENTITY_UNKNOWN")
	}
	return images[0].ID, nil
}
