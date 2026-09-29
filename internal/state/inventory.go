package state

import (
	"database/sql"
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/ziqx/ziqx-dockyard-service/internal/model"
)

func (s *Store) migrateInventory() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 3 {
		return tx.Commit()
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS inventory(id INTEGER PRIMARY KEY CHECK(id=1),data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS inventory_ports(port INTEGER PRIMARY KEY CHECK(port BETWEEN 1 AND 65535)); PRAGMA user_version=3;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func emptyInventory() model.Inventory {
	return model.Inventory{Projects: []model.ExistingProject{}, Sites: []model.ExistingSite{}, ReservedPorts: []int{}, Warnings: []string{}}
}

func (s *Store) Inventory() (model.Inventory, error) {
	v := emptyInventory()
	var b []byte
	err := s.DB.QueryRow("SELECT data FROM inventory WHERE id=1").Scan(&b)
	if err == sql.ErrNoRows {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}

// Preserve each last successful source on failed scans. Observed ports remain
// reserved even after disappearing, just like managed retired reservations.
func (s *Store) SyncInventory(scan model.Inventory, projectsOK, caddyOK, dockerOK bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v := emptyInventory()
	var b []byte
	err = tx.QueryRow("SELECT data FROM inventory WHERE id=1").Scan(&b)
	if err == nil {
		if err = json.Unmarshal(b, &v); err != nil {
			return err
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	now := time.Now().UTC()
	v.LastSyncAt = &now
	v.Warnings = scan.Warnings
	if projectsOK {
		old := map[string]model.ExistingProject{}
		for _, p := range v.Projects {
			p.Present = false
			old[p.ID] = p
		}
		for _, p := range scan.Projects {
			if previous, ok := old[p.ID]; ok && len(p.Warnings) > 0 {
				p.Services = previous.Services
			}
			old[p.ID] = p
		}
		v.Projects = []model.ExistingProject{}
		for _, p := range old {
			v.Projects = append(v.Projects, p)
		}
		sort.Slice(v.Projects, func(i, j int) bool { return v.Projects[i].ID < v.Projects[j].ID })
		v.ProjectsObservedAt = &now
	}
	if caddyOK {
		v.Sites = scan.Sites
		v.CaddyObservedAt = &now
	}
	if dockerOK {
		v.DockerObservedAt = &now
	}
	// Associate only unambiguous local published ports; never infer ownership.
	owners := map[int]map[string]bool{}
	for _, p := range v.Projects {
		if !p.Present {
			continue
		}
		for _, service := range p.Services {
			for _, port := range service.PublishedPorts {
				if owners[port] == nil {
					owners[port] = map[string]bool{}
				}
				owners[port][p.ID] = true
			}
		}
	}
	for i := range v.Sites {
		ids := map[string]bool{}
		for _, dial := range v.Sites[i].Upstreams {
			host, portText, err := net.SplitHostPort(dial)
			if err != nil {
				continue
			}
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				continue
			}
			port, err := strconv.Atoi(portText)
			if err != nil || len(owners[port]) != 1 {
				continue
			}
			for id := range owners[port] {
				ids[id] = true
			}
		}
		v.Sites[i].ProjectIDs = []string{}
		for id := range ids {
			v.Sites[i].ProjectIDs = append(v.Sites[i].ProjectIDs, id)
		}
		sort.Strings(v.Sites[i].ProjectIDs)
	}
	for _, port := range scan.ReservedPorts {
		if _, err = tx.Exec("INSERT OR IGNORE INTO inventory_ports(port) VALUES(?)", port); err != nil {
			return err
		}
	}
	rows, err := tx.Query("SELECT port FROM inventory_ports ORDER BY port")
	if err != nil {
		return err
	}
	v.ReservedPorts = []int{}
	for rows.Next() {
		var port int
		if err = rows.Scan(&port); err != nil {
			rows.Close()
			return err
		}
		v.ReservedPorts = append(v.ReservedPorts, port)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	b, err = json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO inventory(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", b); err != nil {
		return err
	}
	return tx.Commit()
}
