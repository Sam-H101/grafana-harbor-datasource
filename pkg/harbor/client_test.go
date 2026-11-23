package harbor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sam-H101/grafana-harbor-datasource/pkg/harbor"
	"github.com/Sam-H101/grafana-harbor-datasource/pkg/harbor/harbortest"
)

var ctx = context.Background()

func client(t *testing.T, f *harbortest.Fake, user, pass string) *harbor.Client {
	t.Helper()
	c, err := harbor.NewClient(harbor.Config{BaseURL: f.URL, Username: user, Password: pass}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fake(t *testing.T) (*harbor.Client, *harbortest.Fake) {
	t.Helper()
	f := harbortest.New(t)
	f.Username, f.Password = "robot$grafana", "secret"
	return client(t, f, "robot$grafana", "secret"), f
}

func TestNewClientRejectsABadURL(t *testing.T) {
	for _, u := range []string{"", "harbor", "/path"} {
		if _, err := harbor.NewClient(harbor.Config{BaseURL: u}, nil); err == nil {
			t.Errorf("%q was accepted", u)
		}
	}
}

func TestSystemInfoAndCredentials(t *testing.T) {
	c, f := fake(t)
	info, err := c.SystemInfo(ctx)
	if err != nil || !strings.HasPrefix(info.Version, "v2.15.") || info.AuthMode != "db_auth" || info.ReadOnly {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if got := f.Calls("systeminfo")[0].Auth; !strings.HasPrefix(got, "Basic ") {
		t.Errorf("Authorization = %q", got)
	}

	// Without a login Harbor answers, but leaves the version out.
	anon := client(t, f, "", "")
	info, err = anon.SystemInfo(ctx)
	if err != nil || info.Version != "" || anon.HasCredentials() {
		t.Errorf("anonymous info = %+v, %v", info, err)
	}

	// A wrong password is not refused where no login is needed...
	wrong := client(t, f, "robot$grafana", "nope")
	projects, err := wrong.Projects(ctx)
	if err != nil || len(projects) != 3 {
		t.Errorf("wrong password sees %d projects, %v; want the 3 public ones", len(projects), err)
	}
	// ...but the statistics need one.
	if _, err := wrong.Statistics(ctx); !errors.Is(err, harbor.ErrUnauthorized) {
		t.Errorf("statistics with a wrong password: %v", err)
	}
	if _, err := c.Statistics(ctx); err != nil {
		t.Errorf("statistics: %v", err)
	}
}

func TestErrors(t *testing.T) {
	c, f := fake(t)

	f.Forbidden["quotas"] = true
	if _, err := c.Quotas(ctx); !errors.Is(err, harbor.ErrForbidden) {
		t.Errorf("403: %v", err)
	}

	f.Missing["robots"] = true
	_, err := c.Robots(ctx)
	var apiErr *harbor.APIError
	if !errors.As(err, &apiErr) || !apiErr.NotFound() || apiErr.Code != "NOT_FOUND" || !strings.Contains(apiErr.Message, "was not found") || !harbor.IsNotFound(err) {
		t.Errorf("404: %#v", err)
	}

	f.Handlers["jobservice/queues"] = func(w http.ResponseWriter, _ *http.Request) {
		harbortest.Error(w, http.StatusInternalServerError, "UNKNOWN", "internal   server\nerror")
	}
	if _, err := c.JobQueues(ctx); !errors.As(err, &apiErr) || apiErr.Message != "internal server error" || apiErr.Status != 500 {
		t.Errorf("500: %#v", err)
	}

	// Something that is not Harbor: a 404 page, HTML with status 200, a bare 502.
	for name, handler := range map[string]http.HandlerFunc{
		"html 404": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"html 200": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>login</html>")) },
	} {
		f.Handlers["health"] = handler
		if _, err := c.Health(ctx); !errors.Is(err, harbor.ErrNotHarbor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.Handlers["health"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }
	if _, err := c.Health(ctx); !errors.As(err, &apiErr) || apiErr.Message != "Harbor answered HTTP 502" {
		t.Errorf("502: %#v", err)
	}
	// JSON, but not Harbor's system information.
	f.Handlers["systeminfo"] = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"version":"1"}`)) }
	if _, err := c.SystemInfo(ctx); !errors.Is(err, harbor.ErrNotHarbor) {
		t.Errorf("other JSON: %v", err)
	}
}

func TestListFollowsPagesAndStopsAtTheLimit(t *testing.T) {
	c, f := fake(t)
	const total = 250
	f.Handlers["repositories"] = func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		var out []map[string]any
		for i := (page - 1) * size; i < page*size && i < total; i++ {
			out = append(out, map[string]any{"name": fmt.Sprintf("p/r%03d", i)})
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
		_ = json.NewEncoder(w).Encode(out)
	}

	all, more, err := c.Repositories(ctx, harbor.RepositoryFilter{}, 0)
	if err != nil || len(all) != total || more || all[249].Name != "p/r249" {
		t.Fatalf("all: %d, more %v, %v", len(all), more, err)
	}
	if n := len(f.Calls("repositories")); n != 3 {
		t.Errorf("%d requests for 250 items, want 3", n)
	}

	f.Reset()
	some, more, err := c.Repositories(ctx, harbor.RepositoryFilter{}, 120)
	if err != nil || len(some) != 120 || !more {
		t.Errorf("limit 120: %d, more %v, %v", len(some), more, err)
	}
	if n := len(f.Calls("repositories")); n != 2 {
		t.Errorf("%d requests for a limit of 120, want 2", n)
	}

	f.Reset()
	few, more, err := c.Repositories(ctx, harbor.RepositoryFilter{}, 7)
	if err != nil || len(few) != 7 || !more || f.Calls("repositories")[0].Query.Get("page_size") != "7" {
		t.Errorf("limit 7: %d, more %v, %v", len(few), more, err)
	}

	exact, more, err := c.Repositories(ctx, harbor.RepositoryFilter{}, total)
	if err != nil || len(exact) != total || more {
		t.Errorf("limit equal to the total: %d, more %v, %v", len(exact), more, err)
	}
}

func TestProjectsAndQuotas(t *testing.T) {
	c, _ := fake(t)
	projects, err := c.Projects(ctx)
	if err != nil || len(projects) != 5 {
		t.Fatalf("projects: %d, %v", len(projects), err)
	}
	byName := map[string]harbor.Project{}
	for _, p := range projects {
		byName[p.Name] = p
	}
	if p := byName["platform"]; p.Public() || p.RepoCount != 1 || p.OwnerName != "admin" || p.Created.IsZero() || p.ID == 0 {
		t.Errorf("platform = %+v", p)
	}
	if !byName["library"].Public() {
		t.Error("library should be public")
	}

	quotas, err := c.Quotas(ctx)
	if err != nil || len(quotas) != 5 {
		t.Fatalf("quotas: %d, %v", len(quotas), err)
	}
	for _, q := range quotas {
		if q.Ref.Name == "platform" && (q.Hard.Storage != 500*1024*1024 || q.Used.Storage < 1e6 || q.Ref.ID != byName["platform"].ID) {
			t.Errorf("platform quota = %+v", q)
		}
		if q.Ref.Name == "apps" && q.Hard.Storage != -1 {
			t.Errorf("apps quota = %+v", q)
		}
	}
}

func TestRepositories(t *testing.T) {
	c, f := fake(t)
	repos, more, err := c.Repositories(ctx, harbor.RepositoryFilter{Project: "apps", NameContains: "ngi", Sort: "-pull_count"}, 50)
	if err != nil || more || len(repos) != 1 || repos[0].Name != "apps/nginx" || repos[0].ArtifactCount != 1 || repos[0].PullCount < 1 {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
	if project, name := repos[0].Split(); project != "apps" || name != "nginx" {
		t.Errorf("split = %q %q", project, name)
	}
	call := f.Calls("projects/apps/repositories")[0]
	if call.Query.Get("q") != "name=~ngi" || call.Query.Get("sort") != "-pull_count" || call.Query.Get("page_size") != "50" {
		t.Errorf("query = %v", call.Query)
	}

	// A project that does not exist is "forbidden" on Harbor.
	if _, _, err := c.Repositories(ctx, harbor.RepositoryFilter{Project: "nope"}, 0); !errors.Is(err, harbor.ErrForbidden) {
		t.Errorf("unknown project: %v", err)
	}
}

func TestArtifacts(t *testing.T) {
	c, f := fake(t)
	artifacts, _, err := c.Artifacts(ctx, "platform", "alpine", 0)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("artifacts: %d, %v", len(artifacts), err)
	}
	var stable harbor.Artifact
	for _, a := range artifacts {
		for _, tag := range a.Tags {
			if tag.Name == "stable" {
				stable = a
			}
		}
	}
	if len(stable.Tags) != 2 || !strings.HasPrefix(stable.Digest, "sha256:") || stable.Size < 1e6 || stable.Type != "IMAGE" || stable.ExtraAttrs.OS != "linux" {
		t.Errorf("stable = %+v", stable)
	}
	if stable.PushTime.Ptr() == nil || stable.PullTime.Ptr() == nil {
		t.Errorf("times: pushed %v, pulled %v", stable.PushTime, stable.PullTime)
	}
	scan := stable.Scan()
	if scan == nil || scan.Status != "Success" || scan.Severity != "High" || scan.Summary.Total != 2 || scan.Summary.Summary["High"] != 2 || scan.Summary.Fixable != 2 || scan.Scanner.Name != "Trivy" {
		t.Errorf("scan = %+v", scan)
	}

	// The slash of a nested repository name is encoded twice.
	if _, _, err := c.Artifacts(ctx, "apps", "team/api", 0); err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls("projects/apps/repositories/team%252Fapi/artifacts"); len(calls) != 1 || calls[0].Query.Get("with_scan_overview") != "true" || calls[0].Query.Get("sort") != "-push_time" {
		t.Errorf("nested repository: %+v", calls)
	}
}

func TestTimeTreatsYearOneAsUnset(t *testing.T) {
	var v struct {
		Never, Null, Empty, Real harbor.Time
	}
	if err := json.Unmarshal([]byte(`{"Never":"0001-01-01T00:00:00.000Z","Null":null,"Empty":"","Real":"2026-11-23T03:34:08.875Z"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Never.Ptr() != nil || v.Null.Ptr() != nil || v.Empty.Ptr() != nil {
		t.Errorf("unset times: %v %v %v", v.Never, v.Null, v.Empty)
	}
	if want := time.Date(2026, 11, 23, 3, 34, 8, 875e6, time.UTC); v.Real.Ptr() == nil || !v.Real.Ptr().Equal(want) {
		t.Errorf("real = %v", v.Real)
	}
	if err := json.Unmarshal([]byte(`{"Real":"yesterday"}`), &v); err == nil {
		t.Error("a bad time was accepted")
	}
}

func TestSecurityHub(t *testing.T) {
	c, f := fake(t)
	s, err := c.SecuritySummary(ctx)
	if err != nil || s.Critical != 10 || s.High != 54 || s.Medium != 30 || s.Low != 6 || s.Total != 100 || s.Fixable != 100 || s.Scanned != 6 || s.TotalArtifacts != 6 {
		t.Errorf("summary = %+v, %v", s, err)
	}

	vulns, _, err := c.Vulnerabilities(ctx, harbor.VulnerabilityFilter{Severity: "Critical", Repository: "apps/nginx", ProjectID: 3}, 10)
	if err != nil || len(vulns) == 0 {
		t.Fatalf("vulnerabilities: %d, %v", len(vulns), err)
	}
	v := vulns[0]
	if !strings.HasPrefix(v.CVE, "CVE-") || v.Severity != "Critical" || v.CVSS3 < 9 || v.Package == "" || v.FixedVersion == "" || v.RepositoryName != "apps/nginx" || len(v.Tags) != 1 || len(v.Links) == 0 || v.Description == "" {
		t.Errorf("vulnerability = %+v", v)
	}
	q := f.Calls("security/vul")[0].Query
	if q.Get("q") != "severity=Critical,repository_name=apps/nginx,project_id=3" || q.Get("with_tag") != "true" || q.Get("tune_count") != "true" {
		t.Errorf("query = %v", q)
	}

	// Harbor does not count beyond 1000 findings and says -1: a full page
	// then means there may be more.
	f.Handlers["security/vul"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Total-Count", "-1")
		_, _ = w.Write(harbortest.Fixture("security_vul.json"))
	}
	if vulns, more, err := c.Vulnerabilities(ctx, harbor.VulnerabilityFilter{}, 5); err != nil || len(vulns) != 5 || !more {
		t.Errorf("uncounted: %d, more %v, %v", len(vulns), more, err)
	}
}

func TestReplicationAndGarbageCollection(t *testing.T) {
	c, f := fake(t)
	policies, err := c.ReplicationPolicies(ctx)
	if err != nil || len(policies) != 1 || policies[0].Name != "mirror-library" || !policies[0].Enabled {
		t.Fatalf("policies = %+v, %v", policies, err)
	}
	runs, _, err := c.ReplicationExecutions(ctx, policies[0].ID, "Succeed", 20)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if r := runs[0]; r.Status != "Succeed" || r.Trigger != "manual" || r.Total != 1 || r.Succeed != 1 || r.Failed != 0 || r.Start.IsZero() || !r.End.After(r.Start.Time) {
		t.Errorf("run = %+v", r)
	}
	if q := f.Calls("replication/executions")[0].Query; q.Get("policy_id") != "1" || q.Get("status") != "Succeed" {
		t.Errorf("query = %v", q)
	}

	gc, _, err := c.GCHistory(ctx, 10)
	if err != nil || len(gc) != 1 || gc[0].Status != "Success" || gc[0].Kind != "MANUAL" {
		t.Fatalf("gc = %+v, %v", gc, err)
	}
	if res := gc[0].Result(); !res.DryRun || res.DeleteUntagged || res.FreedSpace != 0 {
		t.Errorf("result = %+v", res)
	}
	if res := (harbor.GCRun{Parameters: "not json"}).Result(); res != (harbor.GCResult{}) {
		t.Errorf("unparsable parameters = %+v", res)
	}
}

func TestAuditLogs(t *testing.T) {
	c, f := fake(t)
	from := time.Date(2026, 11, 22, 21, 0, 0, 0, time.FixedZone("x", 3600))
	entries, _, err := c.AuditLogs(ctx, harbor.AuditFilter{From: from, To: from.Add(6 * time.Hour), Username: "admin", Operation: "create", ResourceType: "robot"}, 50)
	if err != nil || len(entries) == 0 {
		t.Fatalf("entries: %d, %v", len(entries), err)
	}
	if e := entries[0]; e.Username != "admin" || e.Operation == "" || e.Resource == "" || e.Time.IsZero() || e.Description == "" || e.Succeeded == nil || !*e.Succeeded {
		t.Errorf("entry = %+v", e)
	}
	q := f.Calls("auditlog-exts")[0].Query
	if q.Get("q") != "op_time=[2026-11-22 20:00:00~2026-11-23 02:00:00],username=admin,operation=create,resource_type=robot" || q.Get("sort") != "-op_time" {
		t.Errorf("query = %v", q)
	}

	// Before Harbor 2.13 the log is at another address.
	f.Missing["auditlog-exts"] = true
	entries, _, err = c.AuditLogs(ctx, harbor.AuditFilter{}, 50)
	if err != nil || len(entries) == 0 || len(f.Calls("audit-logs")) != 1 {
		t.Errorf("old endpoint: %d entries, %v", len(entries), err)
	}
	if q := f.Calls("audit-logs")[0].Query; q.Has("q") {
		t.Errorf("no filter was asked for, got %v", q)
	}
}

func TestQueuesRobotsAndVolumes(t *testing.T) {
	c, _ := fake(t)
	queues, err := c.JobQueues(ctx)
	if err != nil || len(queues) < 10 {
		t.Fatalf("queues: %d, %v", len(queues), err)
	}
	robots, err := c.Robots(ctx)
	if err != nil || len(robots) != 2 || robots[0].Level != "system" || robots[0].Expires() != nil || robots[0].Disabled {
		t.Errorf("robots = %+v, %v", robots, err)
	}
	if exp := (harbor.Robot{ExpiresAt: 1800000000}).Expires(); exp == nil || exp.Year() != 2027 {
		t.Errorf("expiry = %v", exp)
	}
	volumes, err := c.Volumes(ctx)
	if err != nil || len(volumes) != 1 || volumes[0].Total <= volumes[0].Free || volumes[0].Free <= 0 {
		t.Errorf("volumes = %+v, %v", volumes, err)
	}
	h, err := c.Health(ctx)
	if err != nil || h.Status != "healthy" || len(h.Components) != 8 {
		t.Errorf("health = %+v, %v", h, err)
	}
}
