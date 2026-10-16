package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/config"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/process"
)

func TestValidateNamedEnvFiles(t *testing.T) {
	for _, name := range []string{".env.frontend", ".env.backend", ".env.production.local", ".env." + strings.Repeat("a", 64)} {
		if !ValidEnvFilename(name) || ValidateEnvFiles("", map[string]string{name: ""}) != nil {
			t.Fatalf("valid filename rejected: %q", name)
		}
	}
	for _, name := range []string{".env", ".env.", ".env..backend", ".env.foo..bar", "../.env.backend", ".env.api/path", `.env.api\path`, "/tmp/.env.backend", ".env.backend secret", ".env." + strings.Repeat("a", 65)} {
		if ValidEnvFilename(name) || ValidateEnvFiles("", map[string]string{name: ""}) == nil {
			t.Fatalf("unsafe filename accepted: %q", name)
		}
	}
	for _, files := range []map[string]string{
		{".env.backend": "TOKEN=bad\x00value"},
		{".env.backend": strings.Repeat("x", maxEnvBytes+1)},
		{".env.frontend": strings.Repeat("x", maxEnvBytes/2), ".env.backend": strings.Repeat("x", maxEnvBytes/2)},
	} {
		if ValidateEnvFiles("X=1", files) == nil {
			t.Fatal("accepted invalid or oversized environment")
		}
	}
	files := map[string]string{}
	for i := 0; i < maxNamedEnvFiles+1; i++ {
		files[fmt.Sprintf(".env.service%d", i)] = ""
	}
	if ValidateEnvFiles("", files) == nil {
		t.Fatal("accepted too many files")
	}
}

func TestNamedEnvDraftHashIncludesFilesAndPreservesNewerDraft(t *testing.T) {
	c := config.Config{ProjectsRoot: t.TempDir()}
	p := model.Project{ID: "demo", Mode: "compose"}
	files := map[string]string{".env.frontend": "URL=https://frontend.example.com\n", ".env.backend": ""}
	sha, err := SaveDraft(c, p, "services: supplied", "TOKEN=main\n", files)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadDraftEnvFiles(c, p)
	if err != nil || !reflect.DeepEqual(read, files) {
		t.Fatal("draft files not preserved", read, err)
	}
	if st, err := os.Stat(filepath.Join(draftDir(c, p), "draft.json")); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("draft environment is not private", err)
	}
	// Hashing the same files is independent of insertion order.
	ordered := map[string]string{".env.backend": "", ".env.frontend": files[".env.frontend"]}
	if sha != draftSHA("services: supplied", "TOKEN=main\n", ordered) {
		t.Fatal("draft hash depends on map order")
	}
	files[".env.backend"] = "SECRET=new-private-value\n"
	latest, err := SaveDraft(c, p, "services: supplied", "TOKEN=main\n", files)
	if err != nil || latest == sha {
		t.Fatal("named change did not change draft identity", err)
	}
	ClearDraft(c, p, sha)
	if _, _, ok := ReadDraft(c, p); !ok {
		t.Fatal("earlier deployment cleared newer named environment")
	}
	ClearDraft(c, p, latest)
	if _, _, ok := ReadDraft(c, p); ok {
		t.Fatal("deployed draft was retained")
	}
}

func TestDiscoverNamedEnvFilesRejectsLinksAndUsesManagedValues(t *testing.T) {
	c := config.Config{ProjectsRoot: t.TempDir()}
	p := model.Project{ID: "demo", Mode: "compose"}
	if err := Compose(c, p); err != nil {
		t.Fatal(err)
	}
	dir := ProjectWorkingDirectory(c, p)
	for name, value := range map[string]string{".env.frontend": "TOKEN=old\n", ".env.backend": "", "ignored.env": "IGNORE=1\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := DiscoverEnvFiles(c, p, map[string]string{".env.frontend": "TOKEN=saved\n"})
	if err != nil || !reflect.DeepEqual(files, map[string]string{".env.frontend": "TOKEN=saved\n", ".env.backend": ""}) {
		t.Fatal("discovery lost empty file or replaced saved values", files, err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("not a project environment"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".env.symlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverEnvFiles(c, p, nil); err == nil {
		t.Fatal("discovered a symlink")
	}
}

func TestNamedEnvSnapshotIntegrityAndMirrors(t *testing.T) {
	d, p, _ := runtimeServiceFixture(t)
	files := map[string]string{".env.frontend": "FRONTEND_TOKEN=frontend-private\n", ".env.backend": ""}
	r, err := d.PrepareNative(context.Background(), p, "services: supplied", "TOKEN=main\n", files)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadReleaseEnvFiles(d.Config, p, r)
	if err != nil || !reflect.DeepEqual(read, files) {
		t.Fatal("release inputs lost", read, err)
	}
	path := filepath.Join(namedEnvDir(d.Config, p, r.Environment), ".env.frontend")
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("snapshot environment is not private", err)
	}
	secrets, err := d.secrets(p)
	if err != nil || !strings.Contains(strings.Join(secrets, "\n"), "frontend-private") {
		t.Fatal("named secrets omitted from log redaction", err)
	}
	if err := MirrorEnvFiles(d.Config, p, r); err != nil {
		t.Fatal(err)
	}
	for name, value := range files {
		b, err := os.ReadFile(filepath.Join(ProjectWorkingDirectory(d.Config, p), name))
		if err != nil || string(b) != value {
			t.Fatal("mirror differs from snapshot", name, err)
		}
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := nativeRelease(d.Config, p, r); err == nil {
		t.Fatal("tampered named snapshot accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ProjectWorkingDirectory(d.Config, p), ".env.frontend"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReleaseEnvFiles(d.Config, p, r); err == nil {
		t.Fatal("symlink snapshot accepted")
	}
}

// Exercise Docker's real resolver, without a Docker daemon: interpolation uses
// .env, each service gets its selected env_file, and old releases stay frozen.
func TestNativeNamedEnvRoundTrip(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("set DOCKYARD_COMPOSE_TEST=1 for the installed Compose parser")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	c := config.Config{ServerID: "vps-01", ProjectsRoot: t.TempDir(), DockerBinary: binary}
	p := model.Project{ID: "named", Mode: "compose", Environment: model.Production, Slots: map[string]model.Release{}}
	if err := Compose(c, p); err != nil {
		t.Fatal(err)
	}
	d := Docker{Config: c, Runner: process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(home, ".docker")}}
	source := `services:
  frontend:
    image: nginx:${TAG}
    env_file: ${FRONTEND_ENV:-.env.frontend}
  backend:
    image: busybox:latest
    env_file: [.env.backend, .env.empty]
  worker:
    image: busybox:latest
`
	files := map[string]string{
		".env.frontend": "SERVICE=frontend\nTOKEN='frontend$secret'\n",
		".env.backend":  "SERVICE=backend\nTOKEN='backend$secret'\n",
		".env.empty":    "",
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(ProjectWorkingDirectory(c, p), name), []byte("SERVICE=old-host-mirror\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := d.PrepareNative(context.Background(), p, source, "TAG=alpine\nSHARED=worker-only\n", files)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := nativeRelease(c, p, r)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(b, &doc)
	for _, name := range []string{"frontend", "backend"} {
		svc := object(object(doc["services"])[name])
		env := object(svc["environment"])
		if env["SERVICE"] != name || env["TOKEN"] != name+"$$secret" || env["SHARED"] != nil {
			t.Fatalf("%s did not use its own named file: %#v", name, env)
		}
	}
	if object(object(doc["services"])["frontend"])["image"] != "nginx:alpine" || object(object(object(doc["services"])["worker"])["environment"])["SHARED"] != "worker-only" {
		t.Fatal("main .env interpolation/default environment changed")
	}
	for name := range files {
		b, err := os.ReadFile(filepath.Join(ProjectWorkingDirectory(c, p), name))
		if err != nil || string(b) != "SERVICE=old-host-mirror\n" {
			t.Fatal("preview changed a host mirror", name, err)
		}
	}
	p.Slots["blue"] = r
	files[".env.backend"] = "SERVICE=backend-new\n"
	sha, err := SaveDraft(c, p, source, "TAG=alpine\nSHARED=worker-new\n", files)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := PrepareRestartEnvironment(context.Background(), c, d, p, r)
	if err != nil {
		t.Fatal("restart did not resolve saved named files", err)
	}
	if restartedFiles, err := ReadReleaseEnvFiles(c, p, restarted); err != nil || !reflect.DeepEqual(restartedFiles, files) {
		t.Fatal("restart did not retain named snapshot identity", err)
	}
	next, applied, err := PrepareLatestNative(context.Background(), c, d, p, r)
	if err != nil || applied != sha {
		t.Fatal("latest did not apply named draft", err)
	}
	read, err := ReadReleaseEnvFiles(c, p, next)
	if err != nil || !reflect.DeepEqual(read, files) {
		t.Fatal("latest lost named files", err)
	}
	old, err := ReadReleaseEnvFiles(c, p, r)
	if err != nil || old[".env.backend"] != "SERVICE=backend\nTOKEN='backend$secret'\n" {
		t.Fatal("new draft changed rollback inputs", err)
	}
	// A full restart with no saved draft uses deployed snapshots, even when a
	// project-root file was edited independently after the deployment.
	ClearDraft(c, p, sha)
	if _, _, err := PrepareLatestNative(context.Background(), c, d, p, r); err != nil {
		t.Fatal("restart lost deployed named snapshots", err)
	}
}

func TestServiceUpdateAppliesNamedOnlyDraftWithRealCompose(t *testing.T) {
	if os.Getenv("DOCKYARD_COMPOSE_TEST") != "1" {
		t.Skip("set DOCKYARD_COMPOSE_TEST=1 for the installed Compose parser")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, mode := range []string{"restart", "seamless"} {
		t.Run(mode, func(t *testing.T) {
			d, p, runner := runtimeServiceFixture(t)
			d.Config.DockerBinary = binary
			d.Runner = serviceEnvironmentComposeRunner{runner, process.Exec{Timeout: 20 * time.Second, Limit: 2 << 20, DockerConfig: filepath.Join(home, ".docker")}}
			source := `services:
  web:
    image: busybox:latest
    env_file: .env.frontend
    healthcheck:
      test: [CMD, 'true']
    networks: [default]
  postgres:
    image: postgres:17
    env_file: .env.backend
networks:
  default:
    name: original_default
`
			files := map[string]string{".env.frontend": "TOKEN=frontend-before\n", ".env.backend": "TOKEN=backend-before\n"}
			old, err := d.PrepareNative(context.Background(), p, source, "SHARED=main-unchanged\n", files)
			if err != nil {
				t.Fatal(err)
			}
			p.Slots["blue"] = old
			runner.image = runner.containers["web"].Image
			files[".env.frontend"] = "TOKEN='frontend$after'\n"
			files[".env.backend"] = "TOKEN=backend-after\n"
			if _, err := SaveDraft(d.Config, p, source, "SHARED=main-unchanged\n", files); err != nil {
				t.Fatal(err)
			}
			plan, err := d.PlanServiceUpdate(context.Background(), p, "web", mode, 3000, 3010, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := d.PrepareServiceEnvironment(context.Background(), p, &plan); err != nil {
				t.Fatal(err)
			}
			if plan.SourceRelease == nil {
				t.Fatal("named-only environment change skipped")
			}
			plan.PreparedImage = runner.image
			if changed, err := d.PullServiceUpdate(context.Background(), p, &plan); err != nil || !changed {
				t.Fatal("same-image update skipped named environment", changed, err)
			}
			_, b, err := nativeRelease(d.Config, p, plan.Instance.Release)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			json.Unmarshal(b, &doc)
			env := object(object(object(doc["services"])[plan.Instance.Name])["environment"])
			if env["TOKEN"] != "frontend$$after" || env["SHARED"] != nil {
				t.Fatal("service did not get its own latest named values", env)
			}
			if mode == "restart" && object(object(object(doc["services"])["postgres"])["environment"])["TOKEN"] != "backend-before" {
				t.Fatal("individual update changed sibling named values")
			}
			read, err := ReadReleaseEnvFiles(d.Config, p, plan.Instance.Release)
			if err != nil || !reflect.DeepEqual(read, files) {
				t.Fatal("service update lost verified named inputs", err)
			}
			secrets, err := d.secrets(p)
			if err != nil || strings.Contains(string(Redact([]byte("frontend$after backend-after"), secrets, 1024, false)), "after") {
				t.Fatal("resolved named-only secrets were not redacted", err)
			}
		})
	}
}
