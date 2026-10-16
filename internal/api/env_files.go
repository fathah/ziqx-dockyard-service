package api

import (
	"context"
	"sort"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/runtime"
)

func configurationResponse(release, compose, main string, files map[string]string, includeFiles, draft bool) map[string]any {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	response := map[string]any{"release_id": release, "compose_yaml": compose, "env_file": main, "env_files_supported": true, "env_file_names": names}
	// Older native clients remove env_file before exposing configuration to the
	// editor. Named contents therefore require a new, explicit private read.
	if includeFiles {
		response["env_files"] = files
	}
	if draft {
		response["draft"] = true
	}
	return response
}

// Read the complete draft in one operation so the editor never combines
// Compose, main .env and named files from different concurrent saves.
func savedConfiguration(c config.Config, p model.Project) (string, string, map[string]string, bool, error) {
	compose, main, files, draft, err := runtime.ReadDraftConfiguration(c, p)
	if err != nil || draft {
		return compose, main, files, draft, err
	}
	if current, ok := p.Current(); ok {
		compose, main, err = runtime.EditableConfiguration(c, p, current)
		if err == nil {
			files, err = runtime.ReadReleaseEnvFiles(c, p, current)
		}
		return compose, main, files, false, err
	}
	return "", "", map[string]string{}, false, nil
}

// Discovery is reserved for configuration reads, where operators may adopt
// existing files into their next explicit save/deploy payload.
func savedEnvFiles(c config.Config, p model.Project) (map[string]string, error) {
	_, _, files, _, err := savedConfiguration(c, p)
	return files, err
}

type namedNativePreparer interface {
	PrepareNative(context.Context, model.Project, string, string, ...map[string]string) (model.Release, error)
}

type legacyNativePreparer interface {
	PrepareNative(context.Context, model.Project, string, string) (model.Release, error)
}

func canPrepareNative(docker any) bool {
	_, named := docker.(namedNativePreparer)
	_, legacy := docker.(legacyNativePreparer)
	return named || legacy
}

// Keep optional adapters compatible while enabling immutable named-file input
// on the runtime Docker adapter. Legacy adapters cannot accept named files.
func prepareNative(ctx context.Context, docker any, p model.Project, source, env string, files map[string]string) (model.Release, error) {
	if d, ok := docker.(namedNativePreparer); ok {
		return d.PrepareNative(ctx, p, source, env, files)
	}
	if d, ok := docker.(legacyNativePreparer); ok && len(files) == 0 {
		return d.PrepareNative(ctx, p, source, env)
	}
	return model.Release{}, model.Fail("COMPOSE_UNAVAILABLE")
}
