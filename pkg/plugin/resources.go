package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/Sam-H101/grafana-harbor-datasource/pkg/harbor"
	"github.com/Sam-H101/grafana-harbor-datasource/pkg/models"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/resource/httpadapter"
)

const (
	projectsTTL     = time.Minute
	resourceTimeout = 20 * time.Second
	// maxEditorRepositories bounds the repository list offered in the editors.
	maxEditorRepositories = 1000
)

// catalogue caches what several queries of one refresh, and the query
// editor, ask for repeatedly.
type catalogue struct {
	mu         sync.Mutex
	projects   []harbor.Project
	projectsAt time.Time
}

func (d *Datasource) projectList(ctx context.Context) ([]harbor.Project, error) {
	d.cache.mu.Lock()
	if d.cache.projects != nil && d.now().Sub(d.cache.projectsAt) < projectsTTL {
		projects := d.cache.projects
		d.cache.mu.Unlock()
		return projects, nil
	}
	d.cache.mu.Unlock()

	projects, err := d.client.Projects(ctx)
	if err != nil {
		return nil, err
	}
	if projects == nil {
		projects = []harbor.Project{}
	}
	d.cache.mu.Lock()
	d.cache.projects, d.cache.projectsAt = projects, d.now()
	d.cache.mu.Unlock()
	return projects, nil
}

// CallResource serves the lists the query editor needs:
//
//	GET /projects                 [{"name","public"}]
//	GET /repositories?project=x   ["project/repository", ...]
//	GET /policies                 ["replication rule name", ...]
func (d *Datasource) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	d.resOnce.Do(func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /projects", d.handleProjects)
		mux.HandleFunc("GET /repositories", d.handleRepositories)
		mux.HandleFunc("GET /policies", d.handlePolicies)
		d.resHandler = httpadapter.New(mux)
	})
	return d.resHandler.CallResource(ctx, req, sender)
}

type projectItem struct {
	Name   string `json:"name"`
	Public bool   `json:"public"`
}

func (d *Datasource) handleProjects(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, ok := d.resourceContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	projects, err := d.projectList(ctx)
	if err != nil {
		d.resourceError(w, needs(err, "projects", permProjects))
		return
	}
	items := make([]projectItem, len(projects))
	for i, p := range projects {
		items[i] = projectItem{Name: p.Name, Public: p.Public()}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Name < items[b].Name })
	writeJSON(w, http.StatusOK, items)
}

func (d *Datasource) handleRepositories(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, ok := d.resourceContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	// The project goes through the same validation as a query's.
	raw, _ := json.Marshal(map[string]any{"queryType": "repositories", "projects": []string{r.URL.Query().Get("project")}})
	qm, err := models.ParseQuery(raw)
	if err != nil {
		d.resourceError(w, err)
		return
	}
	names := []string{}
	targets := qm.Projects
	if len(targets) == 0 {
		targets = []string{""}
	}
	for _, project := range targets {
		repos, _, err := d.client.Repositories(ctx, harbor.RepositoryFilter{Project: project}, maxEditorRepositories)
		if err != nil {
			d.resourceError(w, needs(err, "repositories", permRepositories))
			return
		}
		for _, repo := range repos {
			names = append(names, repo.Name)
		}
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, names)
}

func (d *Datasource) handlePolicies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, ok := d.resourceContext(w, r)
	if !ok {
		return
	}
	defer cancel()
	policies, err := d.client.ReplicationPolicies(ctx)
	if err != nil {
		d.resourceError(w, needs(err, "replication rules", permReplication))
		return
	}
	names := make([]string, len(policies))
	for i, p := range policies {
		names[i] = p.Name
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, names)
}

func (d *Datasource) resourceContext(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc, bool) {
	if d.settingsErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": d.explain(d.settingsErr).message})
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), resourceTimeout)
	return ctx, cancel, true
}

func (d *Datasource) resourceError(w http.ResponseWriter, err error) {
	p := d.explain(err)
	backend.Logger.Warn("Harbor metadata request failed", "error", logSafe(p.message))
	status := http.StatusBadGateway
	if p.status == backend.StatusBadRequest || p.status == backend.StatusUnauthorized || p.status == backend.StatusForbidden {
		status = int(p.status)
	}
	writeJSON(w, status, map[string]string{"message": p.message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
