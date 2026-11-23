// Package harbortest is a fake Harbor for tests. It answers with responses
// captured from Harbor 2.15 (devenv/harbor), and behaves like it where the
// plugin depends on the behaviour: see docs/harbor-api-notes.md.
package harbortest

import (
	"embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

//go:embed testdata/*.json
var testdata embed.FS

// Call is one request the fake received.
type Call struct {
	// Path is the path under /api/v2.0/, as it was sent (still encoded).
	Path  string
	Query url.Values
	Auth  string
}

// Fake is a fake Harbor. Change its fields before making requests.
type Fake struct {
	*httptest.Server
	// Username and Password are the one account that can log in. A request
	// with other credentials, or with none, is anonymous: it sees the public
	// projects and gets 401 wherever Harbor requires a login.
	Username, Password string
	// Forbidden lists the paths (under /api/v2.0/) that answer 403.
	Forbidden map[string]bool
	// Missing lists the paths that answer 404, as on older versions.
	Missing map[string]bool
	// Handlers replace the built-in answer for a path.
	Handlers map[string]http.HandlerFunc

	mu    sync.Mutex
	calls []Call
}

// routes maps a path under /api/v2.0/ to its captured response.
var routes = map[string]string{
	"health":                 "health.json",
	"statistics":             "statistics.json",
	"systeminfo/volumes":     "volumes.json",
	"projects":               "projects.json",
	"quotas":                 "quotas.json",
	"repositories":           "repositories.json",
	"security/summary":       "security_summary.json",
	"security/vul":           "security_vul.json",
	"replication/policies":   "replication_policies.json",
	"replication/executions": "replication_executions.json",
	"system/gc":              "gc.json",
	"auditlog-exts":          "auditlog_exts.json",
	"audit-logs":             "auditlog_exts.json",
	"jobservice/queues":      "job_queues.json",
	"robots":                 "robots.json",
}

// anonymous lists what Harbor answers without a login.
var anonymous = map[string]bool{"health": true, "systeminfo": true, "projects": true, "repositories": true}

// New starts a fake Harbor that is stopped when the test ends.
func New(t testing.TB) *Fake {
	t.Helper()
	f := &Fake{
		Forbidden: map[string]bool{},
		Missing:   map[string]bool{},
		Handlers:  map[string]http.HandlerFunc{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// Calls returns the requests received for a path such as "projects".
func (f *Fake) Calls(path string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.calls {
		if c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// Reset forgets the recorded requests.
func (f *Fake) Reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// Fixture returns a captured response.
func Fixture(name string) []byte {
	b, err := testdata.ReadFile("testdata/" + name)
	if err != nil {
		panic(err)
	}
	return b
}

// Error writes an error the way Harbor does.
func Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": message}}})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/v2.0/")
	if !ok {
		// Harbor's web page: HTML, whatever the path.
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>Harbor</title></head></html>"))
		return
	}
	q := r.URL.Query()
	f.mu.Lock()
	f.calls = append(f.calls, Call{Path: path, Query: q, Auth: r.Header.Get("Authorization")})
	f.mu.Unlock()

	user, pass, sent := r.BasicAuth()
	loggedIn := f.Username == "" || (sent && user == f.Username && pass == f.Password)

	if h, ok := f.Handlers[path]; ok {
		h(w, r)
		return
	}
	inProject := strings.HasPrefix(path, "projects/")
	if !loggedIn && !anonymous[path] && !inProject {
		Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if f.Forbidden[path] {
		Error(w, http.StatusForbidden, "FORBIDDEN", user)
		return
	}
	if f.Missing[path] {
		Error(w, http.StatusNotFound, "NOT_FOUND", "path /api/v2.0/"+path+" was not found")
		return
	}

	switch {
	case path == "systeminfo":
		f.systemInfo(w, loggedIn)
	case path == "projects":
		f.page(w, q, filter(items("projects.json"), func(p map[string]any) bool {
			return loggedIn || p["metadata"].(map[string]any)["public"] == "true"
		}))
	case path == "repositories":
		f.page(w, q, f.repositories(q, "", loggedIn))
	case inProject:
		f.project(w, strings.Split(strings.TrimPrefix(path, "projects/"), "/"), q, loggedIn)
	case path == "security/vul":
		f.page(w, q, filter(items("security_vul.json"), func(v map[string]any) bool {
			for _, cond := range strings.Split(q.Get("q"), ",") {
				key, want, ok := strings.Cut(cond, "=")
				if !ok {
					continue
				}
				got, _ := v[key].(string)
				if n, isNumber := v[key].(float64); isNumber {
					got = strconv.FormatFloat(n, 'f', -1, 64)
				}
				if got != want {
					return false
				}
			}
			return true
		}))
	case path == "replication/executions":
		f.page(w, q, filter(items("replication_executions.json"), func(e map[string]any) bool {
			return q.Get("status") == "" || e["status"] == q.Get("status")
		}))
	default:
		name, ok := routes[path]
		if !ok {
			Error(w, http.StatusNotFound, "NOT_FOUND", "path /api/v2.0/"+path+" was not found")
			return
		}
		body := Fixture(name)
		if body[0] == '[' {
			f.page(w, q, items(name))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// systemInfo leaves out the version for a request that is not logged in.
func (f *Fake) systemInfo(w http.ResponseWriter, loggedIn bool) {
	var info map[string]any
	_ = json.Unmarshal(Fixture("systeminfo.json"), &info)
	if !loggedIn {
		info = map[string]any{"auth_mode": info["auth_mode"], "self_registration": false}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

func (f *Fake) repositories(q url.Values, project string, loggedIn bool) []map[string]any {
	public := map[string]bool{}
	for _, p := range items("projects.json") {
		public[p["name"].(string)] = p["metadata"].(map[string]any)["public"] == "true"
	}
	contains := strings.TrimPrefix(q.Get("q"), "name=~")
	return filter(items("repositories.json"), func(r map[string]any) bool {
		name := r["name"].(string)
		owner, _, _ := strings.Cut(name, "/")
		return (project == "" || owner == project) && (loggedIn || public[owner]) && strings.Contains(name, contains)
	})
}

// project serves projects/{name}/repositories and
// projects/{name}/repositories/{repository}/artifacts.
func (f *Fake) project(w http.ResponseWriter, parts []string, q url.Values, loggedIn bool) {
	known := false
	for _, p := range items("projects.json") {
		known = known || p["name"] == parts[0]
	}
	switch {
	case !known:
		// As on Harbor: forbidden, not "not found".
		Error(w, http.StatusForbidden, "FORBIDDEN", "forbidden")
	case len(parts) == 2 && parts[1] == "repositories":
		f.page(w, q, f.repositories(q, parts[0], loggedIn))
	case len(parts) == 4 && parts[1] == "repositories" && parts[3] == "artifacts":
		// The repository name arrives encoded twice.
		repo, _ := url.PathUnescape(parts[2])
		repo, _ = url.PathUnescape(repo)
		switch parts[0] + "/" + repo {
		case "platform/alpine":
			f.page(w, q, items("artifacts_alpine.json"))
		case "apps/nginx":
			f.page(w, q, items("artifacts_nginx.json"))
		default:
			// As on Harbor: an empty list for a repository that does not exist.
			f.page(w, q, nil)
		}
	default:
		Error(w, http.StatusNotFound, "NOT_FOUND", "not found")
	}
}

func items(fixture string) []map[string]any {
	var out []map[string]any
	if err := json.Unmarshal(Fixture(fixture), &out); err != nil {
		panic(fixture + ": " + err.Error())
	}
	return out
}

func filter(in []map[string]any, keep func(map[string]any) bool) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, item := range in {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

// page writes one page of a listing with Harbor's X-Total-Count header.
func (f *Fake) page(w http.ResponseWriter, q url.Values, all []map[string]any) {
	size, _ := strconv.Atoi(q.Get("page_size"))
	if size <= 0 {
		size = 10
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page <= 0 {
		page = 1
	}
	start := min((page-1)*size, len(all))
	end := min(start+size, len(all))
	out := all[start:end]
	if out == nil {
		out = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Total-Count", strconv.Itoa(len(all)))
	_ = json.NewEncoder(w).Encode(out)
}
