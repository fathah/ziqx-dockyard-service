package state

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"github.com/ziqx/ziqx-dockyard-service/internal/model"
	"github.com/ziqx/ziqx-dockyard-service/internal/secure"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type Store struct{ DB *sql.DB }

func (s *Store) Capacity(maxQueue, maxProjects int, newProject bool) error {
	var n int
	if e := s.DB.QueryRow("SELECT count(*) FROM jobs").Scan(&n); e != nil {
		return e
	}
	if n >= 10000 {
		return model.Fail("HISTORY_LIMIT")
	}
	if e := s.DB.QueryRow("SELECT count(*) FROM jobs WHERE status IN ('queued','running')").Scan(&n); e != nil {
		return e
	}
	if n >= maxQueue {
		return model.Fail("QUEUE_FULL")
	}
	if newProject {
		if e := s.DB.QueryRow("SELECT count(*) FROM projects").Scan(&n); e != nil {
			return e
		}
		if n >= maxProjects {
			return model.Fail("PROJECT_LIMIT")
		}
	}
	return nil
}

func NewID(prefix string) string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}
func Open(dir string) (*Store, error) {
	if e := secure.PrivateDir(dir); e != nil {
		return nil, e
	}
	path := filepath.Join(dir, "state.db")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e == nil {
		f.Close()
	} else if !os.IsExist(e) {
		return nil, e
	}
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe state file")
	}
	u := url.URL{Scheme: "file", Path: path}
	db, e := sql.Open("sqlite3", u.String()+"?_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS jobs(seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id), status TEXT NOT NULL, data BLOB NOT NULL, input BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS requests(key TEXT PRIMARY KEY, request_id TEXT UNIQUE NOT NULL, fingerprint TEXT NOT NULL, job_id TEXT NOT NULL REFERENCES jobs(id));
CREATE TABLE IF NOT EXISTS ports(port INTEGER PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id));
CREATE TABLE IF NOT EXISTS domains(hostname TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id));
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, event BLOB NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS one_pending_project ON jobs(project_id) WHERE status IN ('queued','running','recovery_required');`)
	if e != nil {
		db.Close()
		return nil, e
	}
	s := &Store{DB: db}
	if e = s.migrateMetadata(); e != nil {
		db.Close()
		return nil, e
	}
	if e = s.migrateInventory(); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Project(id string) (model.Project, error) {
	var p model.Project
	var b []byte
	e := s.DB.QueryRow("SELECT data FROM projects WHERE id=?", id).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	return p, e
}
func (s *Store) Projects() ([]model.Project, error) {
	rows, e := s.DB.Query("SELECT data FROM projects ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Project{}
	for rows.Next() {
		var b []byte
		var p model.Project
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &p); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Job(id string) (model.Job, error) {
	var j model.Job
	var b, in []byte
	e := s.DB.QueryRow("SELECT data,input FROM jobs WHERE id=?", id).Scan(&b, &in)
	if e == nil {
		e = json.Unmarshal(b, &j)
	}
	if e == nil {
		e = json.Unmarshal(in, &j.Input)
	}
	return j, e
}
func (s *Store) Replay(key, request, fingerprint string) (model.Job, bool, error) {
	var fp, id string
	e := s.DB.QueryRow("SELECT fingerprint,job_id FROM requests WHERE key=?", key).Scan(&fp, &id)
	if e == nil {
		if fp != fingerprint {
			return model.Job{}, false, model.Fail("IDEMPOTENCY_CONFLICT")
		}
		j, e := s.Job(id)
		return j, true, e
	}
	if e != sql.ErrNoRows {
		return model.Job{}, false, e
	}
	var count int
	e = s.DB.QueryRow("SELECT count(*) FROM requests WHERE request_id=?", request).Scan(&count)
	if e != nil {
		return model.Job{}, false, e
	}
	if count > 0 {
		return model.Job{}, false, model.Fail("REQUEST_ID_CONFLICT")
	}
	return model.Job{}, false, nil
}
func (s *Store) Blocked() (bool, error) {
	var n int
	e := s.DB.QueryRow("SELECT count(*) FROM jobs WHERE status='recovery_required'").Scan(&n)
	return n > 0, e
}
func (s *Store) Busy(id string) (bool, error) {
	var n int
	e := s.DB.QueryRow("SELECT count(*) FROM jobs WHERE project_id=? AND status IN ('running','queued','recovery_required')", id).Scan(&n)
	return n > 0, e
}
func (s *Store) Reserved(port int, except string) (bool, error) {
	var n int
	e := s.DB.QueryRow("SELECT (SELECT count(*) FROM ports WHERE port=? AND project_id!=?) + (SELECT count(*) FROM inventory_ports WHERE port=?)", port, except, port).Scan(&n)
	return n > 0, e
}
func (s *Store) DomainOwner(host string) (string, error) {
	var id string
	e := s.DB.QueryRow("SELECT project_id FROM domains WHERE hostname=?", host).Scan(&id)
	if e == sql.ErrNoRows {
		return "", nil
	}
	return id, e
}

func audit(tx *sql.Tx, j model.Job) error {
	b, e := json.Marshal(struct {
		JobID, ProjectID, Action, Status, Phase, ActorID, RequestID, Code string
		Time                                                              time.Time
	}{j.ID, j.ProjectID, j.Action, j.Status, j.Phase, j.Actor, j.RequestID, j.Error, time.Now().UTC()})
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO audit(event) VALUES(?)", b)
	return e
}

// Admission, reservations, identity, and audit commit together before HTTP 202.
func (s *Store) Accept(j model.Job, p *model.Project, key, fingerprint string, maxQueue, maxProjects int) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow("SELECT count(*) FROM jobs WHERE status IN ('queued','running')").Scan(&n); e != nil {
		return e
	}
	if n >= maxQueue {
		return model.Fail("QUEUE_FULL")
	}
	if p != nil {
		if e = tx.QueryRow("SELECT count(*) FROM projects").Scan(&n); e != nil {
			return e
		}
		if n >= maxProjects {
			return model.Fail("PROJECT_LIMIT")
		}
		b, _ := json.Marshal(p)
		if _, e = tx.Exec("INSERT INTO projects(id,data) VALUES(?,?)", p.ID, b); e != nil {
			return e
		}
		if e = syncMetadata(tx, *p); e != nil {
			return e
		}
	}
	if j.Input.Project != nil {
		candidate := j.Input.Project
		for _, port := range []int{candidate.BluePort, candidate.GreenPort} {
			if port == 0 {
				continue
			}
			if _, e = tx.Exec("INSERT INTO ports(port,project_id) VALUES(?,?) ON CONFLICT(port) DO UPDATE SET project_id=excluded.project_id WHERE ports.project_id=excluded.project_id", port, j.ProjectID); e != nil {
				return e
			}
			var owner string
			if e = tx.QueryRow("SELECT project_id FROM ports WHERE port=?", port).Scan(&owner); e != nil {
				return e
			}
			if owner != j.ProjectID {
				return model.Fail("PORT_IN_USE")
			}
		}
		for _, d := range candidate.Domains {
			var owner string
			e = tx.QueryRow("SELECT project_id FROM domains WHERE hostname=?", d).Scan(&owner)
			if e == nil && owner != j.ProjectID {
				return model.Fail("DOMAIN_IN_USE")
			}
			if e != nil && e != sql.ErrNoRows {
				return e
			}
			if _, e = tx.Exec("INSERT OR IGNORE INTO domains(hostname,project_id) VALUES(?,?)", d, j.ProjectID); e != nil {
				return e
			}
		}
	}
	b, _ := json.Marshal(j)
	in, _ := json.Marshal(j.Input)
	if _, e = tx.Exec("INSERT INTO jobs(id,project_id,status,data,input) VALUES(?,?,?,?,?)", j.ID, j.ProjectID, j.Status, b, in); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO requests(key,request_id,fingerprint,job_id) VALUES(?,?,?,?)", key, j.RequestID, fingerprint, j.ID); e != nil {
		return e
	}
	if e = audit(tx, j); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Update(j model.Job, p *model.Project) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	b, _ := json.Marshal(j)
	in, _ := json.Marshal(j.Input)
	if _, e = tx.Exec("UPDATE jobs SET status=?,data=?,input=? WHERE id=?", j.Status, b, in, j.ID); e != nil {
		return e
	}
	if p != nil {
		b, _ = json.Marshal(p)
		if _, e = tx.Exec("UPDATE projects SET data=? WHERE id=?", b, p.ID); e != nil {
			return e
		}
		if e = syncMetadata(tx, *p); e != nil {
			return e
		}
		if j.Input.Hostname != "" && j.Input.DNSRecordID != "" {
			if _, e = tx.Exec("INSERT INTO dns_records(project_id,hostname,record_id) VALUES(?,?,?) ON CONFLICT(project_id,hostname) DO UPDATE SET record_id=excluded.record_id", p.ID, j.Input.Hostname, j.Input.DNSRecordID); e != nil {
				return e
			}
		}
	}
	if e = audit(tx, j); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Next() (model.Job, error) {
	var id string
	e := s.DB.QueryRow("SELECT id FROM jobs WHERE status='queued' ORDER BY seq LIMIT 1").Scan(&id)
	if e != nil {
		return model.Job{}, e
	}
	return s.Job(id)
}
func (s *Store) InterruptRunning() error {
	rows, e := s.DB.Query("SELECT id FROM jobs WHERE status='running'")
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		j, e := s.Job(id)
		if e != nil {
			return e
		}
		j.Status = "recovery_required"
		j.Error = "JOB_INTERRUPTED"
		if e = s.Update(j, nil); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) Audit(after int64) ([]json.RawMessage, error) {
	rows, e := s.DB.Query("SELECT id,event FROM audit WHERE id>? ORDER BY id LIMIT 100", after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var id int64
		var b []byte
		if e = rows.Scan(&id, &b); e != nil {
			return nil, e
		}
		out = append(out, json.RawMessage(fmt.Sprintf(`{"event_id":%d,"event":%s}`, id, b)))
	}
	return out, rows.Err()
}
