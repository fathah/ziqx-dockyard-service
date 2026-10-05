package process

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
)

// RootDockerConfig is the VPS administrator's Docker login (`docker login` as root).
var RootDockerConfig = "/root/.docker/config.json"

// syncRegistryAuth copies the administrator's registry logins into Dockyard's
// private Docker config, so images root can pull, Dockyard can pull too.
// Root's entry wins for a registry both define; Dockyard-only entries stay.
func syncRegistryAuth(dockerConfig string) {
	if dockerConfig == "" {
		return
	}
	src, err := os.ReadFile(RootDockerConfig)
	if err != nil {
		return
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(src, &root) != nil {
		return
	}
	target := filepath.Join(dockerConfig, "config.json")
	own := map[string]json.RawMessage{}
	if b, err := os.ReadFile(target); err == nil && json.Unmarshal(b, &own) != nil {
		return // never overwrite a config we cannot parse
	}
	before := map[string]json.RawMessage{}
	for k, v := range own {
		before[k] = v
	}
	for _, key := range []string{"auths", "credHelpers"} {
		merged := map[string]json.RawMessage{}
		if raw, ok := own[key]; ok && json.Unmarshal(raw, &merged) != nil {
			continue
		}
		var add map[string]json.RawMessage
		if raw, ok := root[key]; !ok || json.Unmarshal(raw, &add) != nil || len(add) == 0 {
			continue
		}
		for registry, v := range add {
			merged[registry] = v
		}
		b, _ := json.Marshal(merged)
		own[key] = b
	}
	if _, ok := own["credsStore"]; !ok && root["credsStore"] != nil {
		own["credsStore"] = root["credsStore"]
	}
	if reflect.DeepEqual(before, own) {
		return
	}
	b, err := json.MarshalIndent(own, "", "\t")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dockerConfig, ".config-*.json")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Chmod(0600)
	}
	if tmp.Close() != nil || err != nil {
		return
	}
	os.Rename(tmp.Name(), target)
}
