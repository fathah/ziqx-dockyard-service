package state

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

// Versioned additive migration from the original unversioned SQLite schema.
func (s *Store) migrateMetadata() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return errors.New("database schema is newer than this binary")
	}
	if version == 2 {
		return tx.Commit()
	}
	if version == 0 {
		_, err = tx.Exec(`CREATE TABLE compose_revisions(project_id TEXT NOT NULL REFERENCES projects(id), revision TEXT NOT NULL, metadata BLOB NOT NULL, PRIMARY KEY(project_id,revision));
CREATE TABLE project_slots(project_id TEXT NOT NULL REFERENCES projects(id), slot TEXT NOT NULL CHECK(slot IN ('blue','green')), release_id TEXT NOT NULL, compose_revision TEXT NOT NULL, environment_revision TEXT NOT NULL, image TEXT NOT NULL, PRIMARY KEY(project_id,slot));
CREATE TABLE project_domains(hostname TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id));
CREATE INDEX project_domains_owner ON project_domains(project_id);
CREATE INDEX IF NOT EXISTS reserved_domains_owner ON domains(project_id);
CREATE TABLE dns_records(project_id TEXT NOT NULL REFERENCES projects(id), hostname TEXT NOT NULL, record_id TEXT NOT NULL, PRIMARY KEY(project_id,hostname));`)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(`CREATE TABLE project_targets(project_id TEXT PRIMARY KEY REFERENCES projects(id), app_id TEXT NOT NULL, environment TEXT NOT NULL CHECK(environment IN ('development','staging','production')), UNIQUE(app_id,environment));`)
	if err != nil {
		return err
	}
	rows, err := tx.Query("SELECT data FROM projects")
	if err != nil {
		return err
	}
	var projects []model.Project
	for rows.Next() {
		var b []byte
		var p model.Project
		if err = rows.Scan(&b); err == nil {
			err = json.Unmarshal(b, &p)
		}
		if err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range projects {
		legacyTarget(&p)
		b, marshalErr := json.Marshal(p)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec("UPDATE projects SET data=? WHERE id=?", b, p.ID); err != nil {
			return err
		}
		if err = syncMetadata(tx, p); err != nil {
			return err
		}
	}
	// Queued and recovery jobs can carry project snapshots from the old schema.
	rows, err = tx.Query("SELECT id,input FROM jobs")
	if err != nil {
		return err
	}
	inputs := map[string]model.Input{}
	for rows.Next() {
		var id string
		var b []byte
		var input model.Input
		if err = rows.Scan(&id, &b); err == nil {
			err = json.Unmarshal(b, &input)
		}
		if err != nil {
			rows.Close()
			return err
		}
		if input.Project != nil {
			legacyTarget(input.Project)
			inputs[id] = input
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id, input := range inputs {
		if err = input.Project.ValidateTarget(); err != nil {
			return err
		}
		b, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec("UPDATE jobs SET input=? WHERE id=?", b, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("PRAGMA user_version=2"); err != nil {
		return err
	}
	return tx.Commit()
}

// Project snapshots, assigned-domain and slot indexes commit with job/audit state.
func syncMetadata(tx *sql.Tx, p model.Project) error {
	if err := p.ValidateTarget(); err != nil {
		return err
	}
	var app, environment string
	err := tx.QueryRow("SELECT app_id,environment FROM project_targets WHERE project_id=?", p.ID).Scan(&app, &environment)
	if err == nil {
		if app != p.AppID || environment != p.Environment {
			return model.Fail("PROJECT_TARGET_IMMUTABLE")
		}
	} else if err == sql.ErrNoRows {
		var owner string
		err = tx.QueryRow("SELECT project_id FROM project_targets WHERE app_id=? AND environment=?", p.AppID, p.Environment).Scan(&owner)
		if err == nil {
			return model.Fail("APP_ENVIRONMENT_EXISTS")
		}
		if err != sql.ErrNoRows {
			return err
		}
		if _, err = tx.Exec("INSERT INTO project_targets(project_id,app_id,environment) VALUES(?,?,?)", p.ID, p.AppID, p.Environment); err != nil {
			return err
		}
	} else {
		return err
	}
	if _, err := tx.Exec("DELETE FROM project_domains WHERE project_id=?", p.ID); err != nil {
		return err
	}
	for _, host := range p.Domains {
		if _, err := tx.Exec("INSERT INTO project_domains(hostname,project_id) VALUES(?,?)", host, p.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM project_slots WHERE project_id=?", p.ID); err != nil {
		return err
	}
	for slot, r := range p.Slots {
		if _, err := tx.Exec("INSERT INTO project_slots(project_id,slot,release_id,compose_revision,environment_revision,image) VALUES(?,?,?,?,?,?)", p.ID, slot, r.ID, r.Compose, r.Environment, r.Image); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveCompose(project, revision string, services map[string]model.ServiceSpec) error {
	b, err := json.Marshal(services)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRow("SELECT metadata FROM compose_revisions WHERE project_id=? AND revision=?", project, revision).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, b) {
			return model.Uncertain("METADATA_DIVERGED")
		}
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		return err
	}
	if _, err = tx.Exec("INSERT INTO compose_revisions(project_id,revision,metadata) VALUES(?,?,?)", project, revision, b); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ComposeServices(project, revision string) (map[string]model.ServiceSpec, error) {
	var b []byte
	err := s.DB.QueryRow("SELECT metadata FROM compose_revisions WHERE project_id=? AND revision=?", project, revision).Scan(&b)
	if err != nil {
		return nil, err
	}
	var services map[string]model.ServiceSpec
	if err = json.Unmarshal(b, &services); err != nil {
		return nil, err
	}
	return services, nil
}

func (s *Store) Services(project string) ([]model.ServiceInfo, error) {
	rows, err := s.DB.Query(`SELECT s.slot,s.compose_revision,s.environment_revision,s.image,c.metadata FROM project_slots s LEFT JOIN compose_revisions c ON c.project_id=s.project_id AND c.revision=s.compose_revision WHERE s.project_id=? ORDER BY s.slot`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ServiceInfo{}
	for rows.Next() {
		var slot, revision, environment, image string
		var b []byte
		if err = rows.Scan(&slot, &revision, &environment, &image, &b); err != nil {
			return nil, err
		}
		if len(b) == 0 {
			return nil, model.Fail("METADATA_UNAVAILABLE")
		}
		var services map[string]model.ServiceSpec
		if err = json.Unmarshal(b, &services); err != nil {
			return nil, err
		}
		names := []string{}
		for name := range services {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			service := services[name]
			resolved := service.Image
			if revision == "" {
				resolved = image
			}
			out = append(out, model.ServiceInfo{Slot: slot, Name: name, Image: resolved, Template: service.Template, Compose: revision, Environment: environment})
		}
	}
	return out, rows.Err()
}

func (s *Store) Domains(project string) ([]model.DomainInfo, error) {
	rows, err := s.DB.Query(`SELECT h.hostname,CASE WHEN a.hostname IS NULL THEN 0 ELSE 1 END,COALESCE(d.record_id,'') FROM (SELECT hostname FROM domains WHERE project_id=? UNION SELECT hostname FROM project_domains WHERE project_id=?) h LEFT JOIN project_domains a ON a.hostname=h.hostname AND a.project_id=? LEFT JOIN dns_records d ON d.hostname=h.hostname AND d.project_id=? ORDER BY h.hostname`, project, project, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DomainInfo{}
	for rows.Next() {
		var item model.DomainInfo
		if err = rows.Scan(&item.Hostname, &item.Assigned, &item.DNSRecordID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Historical deployments keep their IDs, paths and mode. Production permits
// both existing single-slot and blue-green projects without changing live slots.
func legacyTarget(p *model.Project) {
	if p.AppID == "" {
		p.AppID = p.ID
	}
	if p.Environment == "" {
		p.Environment = model.Production
	}
}

func (s *Store) TargetOwner(app, environment string) (string, error) {
	var id string
	err := s.DB.QueryRow("SELECT project_id FROM project_targets WHERE app_id=? AND environment=?", app, environment).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}
