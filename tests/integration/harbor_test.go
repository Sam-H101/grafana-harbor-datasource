//go:build integration

// Package integration runs the plugin against a real server: the dev stack's
// Harbor with its seeded projects, images, scans and robot accounts
// (devenv/harbor/harbor.sh, devenv/harbor/seed.py).
//
//	HARBOR_URL       default http://proxy:8080
//	HARBOR_SEED_DIR  default /run/harbor: the secrets the seed wrote
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sam-H101/grafana-harbor-datasource/pkg/plugin"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func harborURL() string { return env("HARBOR_URL", "http://proxy:8080") }

func secret(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(env("HARBOR_SEED_DIR", "/run/harbor"), file))
	if err != nil {
		t.Fatalf("no secret: start the dev stack, or set HARBOR_SEED_DIR (%v)", err)
	}
	return strings.TrimSpace(string(b))
}

func datasource(t *testing.T, url, user, pass string) *plugin.Datasource {
	t.Helper()
	js, _ := json.Marshal(map[string]any{"username": user})
	secure := map[string]string{}
	if pass != "" {
		secure["password"] = pass
	}
	inst, err := plugin.NewDatasource(context.Background(), backend.DataSourceInstanceSettings{URL: url, JSONData: js, DecryptedSecureJSONData: secure})
	if err != nil {
		t.Fatal(err)
	}
	return inst.(*plugin.Datasource)
}

// reader has every permission the plugin can use; limited may only list
// projects, repositories and artifacts.
func reader(t *testing.T) *plugin.Datasource {
	return datasource(t, harborURL(), "robot$grafana", secret(t, "secret"))
}

func limited(t *testing.T) *plugin.Datasource {
	return datasource(t, harborURL(), "robot$grafana-limited", secret(t, "limited-secret"))
}

func query(t *testing.T, ds *plugin.Datasource, q map[string]any) backend.DataResponse {
	t.Helper()
	js, _ := json.Marshal(q)
	now := time.Now()
	resp, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{Queries: []backend.DataQuery{{
		RefID: "A", JSON: js, TimeRange: backend.TimeRange{From: now.AddDate(0, 0, -7), To: now.Add(time.Minute)},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Responses["A"]
}

func frame(t *testing.T, ds *plugin.Datasource, q map[string]any) *data.Frame {
	t.Helper()
	r := query(t, ds, q)
	if r.Error != nil {
		t.Fatalf("%v: %v", q, r.Error)
	}
	if len(r.Frames) != 1 {
		t.Fatalf("%v: %d frames", q, len(r.Frames))
	}
	return r.Frames[0]
}

func column(t *testing.T, f *data.Frame, name string) []string {
	t.Helper()
	field, _ := f.FieldByName(name)
	if field == nil {
		names := make([]string, len(f.Fields))
		for i, fl := range f.Fields {
			names[i] = fl.Name
		}
		t.Fatalf("no field %q in %v", name, names)
	}
	out := make([]string, field.Len())
	for i := range out {
		if v, ok := field.ConcreteAt(i); ok {
			out[i] = fmt.Sprint(v)
		}
	}
	return out
}

// row returns the index of the first row whose column has the value.
func row(t *testing.T, f *data.Frame, name, value string) int {
	t.Helper()
	col := column(t, f, name)
	for i, v := range col {
		if v == value {
			return i
		}
	}
	t.Fatalf("no row with %s = %q in %v", name, value, col)
	return -1
}

func number(t *testing.T, s string) float64 {
	t.Helper()
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("%q is not a number", s)
	}
	return n
}

func notices(f *data.Frame) string {
	if f.Meta == nil {
		return ""
	}
	var out []string
	for _, n := range f.Meta.Notices {
		out = append(out, n.Text)
	}
	return strings.Join(out, " | ")
}

// measure returns one number of a frame of measures (statistics, summary).
func measure(t *testing.T, f *data.Frame, name string) float64 {
	t.Helper()
	col := column(t, f, name)
	if len(col) != 1 {
		t.Fatalf("%s has %d values", name, len(col))
	}
	return number(t, col[0])
}

func health(t *testing.T, ds *plugin.Datasource) *backend.CheckHealthResult {
	t.Helper()
	res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestHealth(t *testing.T) {
	res := health(t, reader(t))
	if res.Status != backend.HealthStatusOk {
		t.Fatalf("reader: %s", res.Message)
	}
	for _, want := range []string{"Connected to Harbor v2.", "The account robot$grafana can see 5 projects."} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("reader: %q lacks %q", res.Message, want)
		}
	}
	if strings.Contains(res.Message, "Not readable") {
		t.Errorf("the reader should be able to read everything: %s", res.Message)
	}

	res = health(t, limited(t))
	if res.Status != backend.HealthStatusOk {
		t.Fatalf("limited: %s", res.Message)
	}
	for _, want := range []string{"Not readable with this login", "project quotas (Quota: List (system))", "vulnerabilities", "replications", "garbage collection", "job queues", "robot accounts"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("limited: %q lacks %q", res.Message, want)
		}
	}
}

func TestHealthFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		ds   *plugin.Datasource
		want string
	}{
		"wrong secret":  {datasource(t, harborURL(), "robot$grafana", "Wrong-secret-123"), "rejected the user name or password"},
		"unknown user":  {datasource(t, harborURL(), "nobody", "Wrong-secret-123"), "rejected the user name or password"},
		"not harbor":    {datasource(t, harborURL()+"/c", "", ""), "did not answer like Harbor"},
		"no such host":  {datasource(t, "http://harbor.invalid", "", ""), "host not found"},
		"nothing there": {datasource(t, "http://127.0.0.1:9", "", ""), "Cannot reach Harbor"},
	} {
		res := health(t, tc.ds)
		if res.Status != backend.HealthStatusError || !strings.Contains(res.Message, tc.want) {
			t.Errorf("%s: %v %q, want an error with %q", name, res.Status, res.Message, tc.want)
		}
	}
}

func TestAnonymousSeesPublicProjects(t *testing.T) {
	ds := datasource(t, harborURL(), "", "")
	res := health(t, ds)
	if res.Status != backend.HealthStatusOk || !strings.Contains(res.Message, "Anonymous access can see 3 projects") {
		t.Errorf("health: %v %q", res.Status, res.Message)
	}
	f := frame(t, ds, map[string]any{"queryType": "projects"})
	if got := strings.Join(column(t, f, "project"), ","); got != "library,mirror,sandbox" {
		t.Errorf("projects = %s", got)
	}
	if r := query(t, ds, map[string]any{"queryType": "robots"}); r.Error == nil || !strings.Contains(r.Error.Error(), "requires a login") {
		t.Errorf("robots: %v", r.Error)
	}
}

func TestProjects(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "projects"})
	if got := strings.Join(column(t, f, "project"), ","); got != "apps,library,mirror,platform,sandbox" {
		t.Fatalf("projects = %s", got)
	}
	i := row(t, f, "project", "platform")
	if column(t, f, "public")[i] != "false" || column(t, f, "repositories")[i] != "1" || column(t, f, "owner")[i] != "admin" {
		t.Errorf("platform: public %s, repositories %s", column(t, f, "public")[i], column(t, f, "repositories")[i])
	}
	if quota := number(t, column(t, f, "storage_quota")[i]); quota != 500*1024*1024 {
		t.Errorf("platform quota = %v", quota)
	}
	used := number(t, column(t, f, "storage_used")[i])
	if used < 1e6 || used > 1e8 {
		t.Errorf("platform uses %v bytes", used)
	}
	if pct := number(t, column(t, f, "quota_used")[i]); pct <= 0 || pct >= 100 {
		t.Errorf("platform quota used = %v%%", pct)
	}
	// A project without a quota has no quota and no percentage.
	i = row(t, f, "project", "apps")
	if column(t, f, "storage_quota")[i] != "" || column(t, f, "quota_used")[i] != "" || column(t, f, "storage_used")[i] == "" {
		t.Errorf("apps: quota %q, used %q", column(t, f, "storage_quota")[i], column(t, f, "storage_used")[i])
	}

	f = frame(t, reader(t), map[string]any{"queryType": "projects", "projects": []string{"sandbox", "platform"}})
	if got := strings.Join(column(t, f, "project"), ","); got != "platform,sandbox" {
		t.Errorf("selected projects = %s", got)
	}

	// One number per project, for alert rules.
	f = frame(t, reader(t), map[string]any{"queryType": "projects", "value": "quotaPercent"})
	labels := map[string]bool{}
	for _, field := range f.Fields[1:] {
		labels[field.Labels["project"]] = true
		if v := field.At(0).(float64); v < 0 || v > 100 {
			t.Errorf("%s uses %v%% of its quota", field.Labels["project"], v)
		}
	}
	if len(labels) != 2 || !labels["platform"] || !labels["sandbox"] {
		t.Errorf("projects with a quota = %v", labels)
	}

	r := query(t, reader(t), map[string]any{"queryType": "projects", "projects": []string{"nope"}})
	if r.Error == nil || !strings.Contains(r.Error.Error(), `No such project "nope"`) {
		t.Errorf("unknown project: %v", r.Error)
	}
}

func TestProjectsWithoutQuotaPermission(t *testing.T) {
	f := frame(t, limited(t), map[string]any{"queryType": "projects"})
	if len(column(t, f, "project")) != 5 {
		t.Errorf("projects = %v", column(t, f, "project"))
	}
	if !strings.Contains(notices(f), "Quota: List (system)") {
		t.Errorf("notice = %q", notices(f))
	}
	r := query(t, limited(t), map[string]any{"queryType": "projects", "value": "storage"})
	if r.Error == nil || !strings.Contains(r.Error.Error(), "Quota: List (system)") {
		t.Errorf("storage per project without the permission: %v", r.Error)
	}
}

func TestRepositories(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "repositories", "sort": "name"})
	full := strings.Join(column(t, f, "full_name"), ",")
	if full != "apps/nginx,apps/redis,library/busybox,mirror/busybox,platform/alpine" {
		t.Fatalf("repositories = %s", full)
	}
	i := row(t, f, "full_name", "platform/alpine")
	if column(t, f, "project")[i] != "platform" || column(t, f, "repository")[i] != "alpine" || column(t, f, "artifacts")[i] != "2" {
		t.Errorf("platform/alpine: %v %v %v", column(t, f, "project")[i], column(t, f, "repository")[i], column(t, f, "artifacts")[i])
	}
	if pulls := number(t, column(t, f, "pulls")[row(t, f, "full_name", "apps/nginx")]); pulls < 1 {
		t.Errorf("apps/nginx pulls = %v", pulls)
	}

	f = frame(t, reader(t), map[string]any{"queryType": "repositories", "projects": []string{"apps", "platform"}, "sort": "name", "descending": true})
	if got := strings.Join(column(t, f, "full_name"), ","); got != "platform/alpine,apps/redis,apps/nginx" {
		t.Errorf("two projects, descending = %s", got)
	}

	f = frame(t, reader(t), map[string]any{"queryType": "repositories", "repository": "busy"})
	if got := strings.Join(column(t, f, "repository"), ","); got != "busybox,busybox" {
		t.Errorf("name filter = %s", got)
	}

	f = frame(t, reader(t), map[string]any{"queryType": "repositories", "sort": "pulls", "descending": true, "limit": 2})
	if len(column(t, f, "repository")) != 2 || !strings.Contains(notices(f), "Showing the first 2 repositories") {
		t.Errorf("limit: %v, %q", column(t, f, "repository"), notices(f))
	}
	pulls := column(t, f, "pulls")
	if number(t, pulls[0]) < number(t, pulls[1]) {
		t.Errorf("not sorted by pulls: %v", pulls)
	}
}

func TestArtifacts(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "artifacts", "projects": []string{"platform"}, "repository": "alpine"})
	tags := column(t, f, "tags")
	if len(tags) != 2 {
		t.Fatalf("tags = %v", tags)
	}
	i := row(t, f, "tags", "3.20, stable")
	if !strings.HasPrefix(column(t, f, "digest")[i], "sha256:") || number(t, column(t, f, "size")[i]) < 1e6 {
		t.Errorf("3.20: digest %q, size %q", column(t, f, "digest")[i], column(t, f, "size")[i])
	}
	if column(t, f, "pushed")[i] == "" || column(t, f, "pulled")[i] == "" || column(t, f, "platform")[i] != "linux/amd64" {
		t.Errorf("3.20: pushed %q, pulled %q, platform %q", column(t, f, "pushed")[i], column(t, f, "pulled")[i], column(t, f, "platform")[i])
	}
	i = row(t, f, "tags", "3.10")
	if column(t, f, "scan_status")[i] != "Success" || column(t, f, "severity")[i] == "" || number(t, column(t, f, "vulnerabilities")[i]) < 1 {
		t.Errorf("3.10 scan: %q %q %q", column(t, f, "scan_status")[i], column(t, f, "severity")[i], column(t, f, "vulnerabilities")[i])
	}

	// The full repository name works too.
	f = frame(t, reader(t), map[string]any{"queryType": "artifacts", "projects": []string{"platform"}, "repository": "platform/alpine"})
	if len(column(t, f, "tags")) != 2 {
		t.Errorf("full name: %v", column(t, f, "tags"))
	}

	// Every repository of several projects.
	f = frame(t, reader(t), map[string]any{"queryType": "artifacts", "projects": []string{"apps", "library"}})
	repos := map[string]bool{}
	for i, p := range column(t, f, "project") {
		repos[p+"/"+column(t, f, "repository")[i]] = true
	}
	if len(repos) != 3 || !repos["apps/nginx"] || !repos["apps/redis"] || !repos["library/busybox"] {
		t.Errorf("repositories = %v", repos)
	}
	if nginx := row(t, f, "repository", "nginx"); number(t, column(t, f, "critical")[nginx]) < 1 || number(t, column(t, f, "fixable")[nginx]) < 1 {
		t.Errorf("nginx:1.19 should have critical, fixable findings")
	}

	for name, tc := range map[string]struct {
		q    map[string]any
		want string
	}{
		"no project":         {map[string]any{"queryType": "artifacts"}, "Choose at least one project"},
		"unknown project":    {map[string]any{"queryType": "artifacts", "projects": []string{"nope"}}, `No such project "nope"`},
		"unknown repository": {map[string]any{"queryType": "artifacts", "projects": []string{"apps"}, "repository": "nope"}, "There is no repository apps/nope"},
	} {
		if r := query(t, reader(t), tc.q); r.Error == nil || !strings.Contains(r.Error.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, r.Error, tc.want)
		}
	}
}

func TestVulnerabilities(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "vulnerabilities", "severity": "critical", "limit": 500})
	cves := column(t, f, "cve")
	if len(cves) < 3 {
		t.Fatalf("critical findings = %v", cves)
	}
	for i := range cves {
		if !strings.HasPrefix(cves[i], "CVE-") || column(t, f, "severity")[i] != "Critical" || column(t, f, "package")[i] == "" || column(t, f, "project")[i] == "" {
			t.Errorf("row %d: %s %s %s", i, cves[i], column(t, f, "severity")[i], column(t, f, "package")[i])
		}
	}

	f = frame(t, reader(t), map[string]any{"queryType": "vulnerabilities", "projects": []string{"apps"}, "repository": "apps/nginx", "limit": 3})
	if got := column(t, f, "repository"); len(got) != 3 || got[0] != "nginx" || column(t, f, "project")[0] != "apps" || column(t, f, "tags")[0] != "1.19-alpine" {
		t.Errorf("apps/nginx: %v", got)
	}
	if !strings.Contains(notices(f), "Showing the first 3 vulnerabilities") {
		t.Errorf("notice = %q", notices(f))
	}

	one := cves[0]
	f = frame(t, reader(t), map[string]any{"queryType": "vulnerabilities", "cve": strings.ToLower(one)})
	for _, c := range column(t, f, "cve") {
		if c != one {
			t.Errorf("filter on %s returned %s", one, c)
		}
	}
	if len(column(t, f, "cve")) == 0 {
		t.Errorf("no finding for %s", one)
	}

	f = frame(t, reader(t), map[string]any{"queryType": "vulnerabilities", "projects": []string{"sandbox"}})
	if len(column(t, f, "cve")) != 0 || !strings.Contains(notices(f), "No matching vulnerabilities") {
		t.Errorf("sandbox has no images: %v", column(t, f, "cve"))
	}
}

func TestSecuritySummaryAndStatistics(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "securitySummary"})
	if measure(t, f, "critical") < 3 || measure(t, f, "total") < measure(t, f, "critical") || measure(t, f, "fixable") < 1 {
		t.Errorf("summary: critical %v, total %v", measure(t, f, "critical"), measure(t, f, "total"))
	}
	if measure(t, f, "scanned_artifacts") < 5 || measure(t, f, "artifacts") < measure(t, f, "scanned_artifacts") {
		t.Errorf("scanned %v of %v", measure(t, f, "scanned_artifacts"), measure(t, f, "artifacts"))
	}

	f = frame(t, reader(t), map[string]any{"queryType": "statistics"})
	if measure(t, f, "projects") != 5 || measure(t, f, "public_projects") != 3 || measure(t, f, "private_projects") != 2 || measure(t, f, "repositories") != 5 {
		t.Errorf("projects %v, public %v, repositories %v", measure(t, f, "projects"), measure(t, f, "public_projects"), measure(t, f, "repositories"))
	}
	if measure(t, f, "storage_used") < 1e7 || measure(t, f, "disk_total") <= measure(t, f, "disk_free") || measure(t, f, "disk_free_percent") <= 0 {
		t.Errorf("storage %v, disk %v free of %v", measure(t, f, "storage_used"), measure(t, f, "disk_free"), measure(t, f, "disk_total"))
	}

	// What the account may not read is left out and named.
	f = frame(t, limited(t), map[string]any{"queryType": "statistics"})
	if measure(t, f, "projects") != 5 {
		t.Errorf("limited: projects = %v", measure(t, f, "projects"))
	}
	if field, _ := f.FieldByName("storage_used"); field != nil {
		t.Error("limited: storage used should be left out")
	}
	if !strings.Contains(notices(f), "Quota: List (system)") || !strings.Contains(notices(f), "System volumes: Read (system)") {
		t.Errorf("limited notice = %q", notices(f))
	}
}

func TestComponentHealth(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "health"})
	i := row(t, f, "component", "core")
	if column(t, f, "status")[i] != "healthy" || len(column(t, f, "component")) < 6 {
		t.Errorf("components = %v, core %s", column(t, f, "component"), column(t, f, "status")[i])
	}
	row(t, f, "component", "trivy")

	f = frame(t, reader(t), map[string]any{"queryType": "health", "unhealthyOnly": true})
	if len(column(t, f, "component")) != 0 || !strings.Contains(notices(f), "Every component is healthy") {
		t.Errorf("unhealthy = %v", column(t, f, "component"))
	}

	f = frame(t, reader(t), map[string]any{"queryType": "health", "value": "status"})
	if len(f.Fields) < 7 || f.Fields[1].Labels["component"] == "" || f.Fields[1].At(0).(float64) != 1 {
		t.Errorf("numbers: %d fields", len(f.Fields))
	}
}

func TestReplications(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "replications"})
	if len(column(t, f, "rule")) < 1 {
		t.Fatal("no replication runs")
	}
	last := len(column(t, f, "rule")) - 1
	if column(t, f, "rule")[last] != "mirror-library" || column(t, f, "status")[last] != "Succeed" || column(t, f, "trigger")[last] != "manual" {
		t.Errorf("run: %s %s %s", column(t, f, "rule")[last], column(t, f, "status")[last], column(t, f, "trigger")[last])
	}
	if number(t, column(t, f, "succeeded")[last]) != 1 || number(t, column(t, f, "failed")[last]) != 0 || column(t, f, "started")[last] == "" || column(t, f, "duration")[last] == "" {
		t.Errorf("run: succeeded %s, started %q", column(t, f, "succeeded")[last], column(t, f, "started")[last])
	}

	f = frame(t, reader(t), map[string]any{"queryType": "replications", "policy": "mirror-library", "status": "failed"})
	if len(column(t, f, "rule")) != 0 || !strings.Contains(notices(f), "No matching replication runs") {
		t.Errorf("failed runs = %v, %q", column(t, f, "rule"), notices(f))
	}

	if r := query(t, reader(t), map[string]any{"queryType": "replications", "policy": "nope"}); r.Error == nil || !strings.Contains(r.Error.Error(), `No such replication rule "nope"`) {
		t.Errorf("unknown rule: %v", r.Error)
	}
}

func TestGarbageCollections(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "garbageCollections"})
	if len(column(t, f, "status")) < 1 {
		t.Fatal("no garbage collection runs")
	}
	last := len(column(t, f, "status")) - 1
	if column(t, f, "status")[last] != "Success" || column(t, f, "trigger")[last] != "manual" || column(t, f, "dry_run")[last] != "true" {
		t.Errorf("run: %s %s dry run %s", column(t, f, "status")[last], column(t, f, "trigger")[last], column(t, f, "dry_run")[last])
	}
	if column(t, f, "started")[last] == "" || column(t, f, "duration")[last] == "" || column(t, f, "freed_space")[last] != "0" {
		t.Errorf("run: started %q, duration %q, freed %q", column(t, f, "started")[last], column(t, f, "duration")[last], column(t, f, "freed_space")[last])
	}
}

func TestAuditLog(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "auditLogs", "limit": 500})
	times := column(t, f, "time")
	if len(times) < 10 {
		t.Fatalf("%d audit log entries", len(times))
	}
	// Newest first.
	if times[0] < times[len(times)-1] {
		t.Errorf("not newest first: %s .. %s", times[0], times[len(times)-1])
	}
	found := false
	for i, op := range column(t, f, "operation") {
		if op == "create" && column(t, f, "resource_type")[i] == "project" && column(t, f, "resource")[i] == "platform" {
			found = column(t, f, "user")[i] == "admin" && column(t, f, "description")[i] != ""
		}
	}
	if !found {
		t.Error("the creation of the project platform is not in the audit log")
	}

	f = frame(t, reader(t), map[string]any{"queryType": "auditLogs", "operation": "create", "resourceType": "robot"})
	resources := strings.Join(column(t, f, "resource"), ",")
	if !strings.Contains(resources, "robot$grafana") {
		t.Errorf("robot creations = %s", resources)
	}
	for _, rt := range column(t, f, "resource_type") {
		if rt != "robot" {
			t.Errorf("filter on robot returned %s", rt)
		}
	}

	// Outside the time range there is nothing.
	js, _ := json.Marshal(map[string]any{"queryType": "auditLogs"})
	old := time.Now().AddDate(-2, 0, 0)
	resp, err := reader(t).QueryData(context.Background(), &backend.QueryDataRequest{Queries: []backend.DataQuery{{
		RefID: "A", JSON: js, TimeRange: backend.TimeRange{From: old, To: old.Add(time.Hour)},
	}}})
	if err != nil || resp.Responses["A"].Error != nil {
		t.Fatalf("old range: %v %v", err, resp.Responses["A"].Error)
	}
	if n := resp.Responses["A"].Frames[0].Rows(); n != 0 {
		t.Errorf("%d entries two years ago", n)
	}
}

func TestJobQueues(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "jobQueues"})
	i := row(t, f, "job_type", "IMAGE_SCAN")
	if column(t, f, "paused")[i] != "false" || column(t, f, "pending")[i] == "" {
		t.Errorf("IMAGE_SCAN: paused %s, pending %q", column(t, f, "paused")[i], column(t, f, "pending")[i])
	}
	row(t, f, "job_type", "REPLICATION")

	f = frame(t, reader(t), map[string]any{"queryType": "jobQueues", "value": "pending"})
	if len(f.Fields) < 5 || f.Fields[1].Labels["job_type"] == "" {
		t.Errorf("numbers: %d fields", len(f.Fields))
	}
}

func TestRobots(t *testing.T) {
	f := frame(t, reader(t), map[string]any{"queryType": "robots"})
	i := row(t, f, "name", "robot$grafana")
	if column(t, f, "level")[i] != "system" || column(t, f, "enabled")[i] != "true" || column(t, f, "expires")[i] != "" || column(t, f, "days_left")[i] != "" {
		t.Errorf("robot$grafana: %s enabled %s expires %q", column(t, f, "level")[i], column(t, f, "enabled")[i], column(t, f, "expires")[i])
	}
	row(t, f, "name", "robot$grafana-limited")
}

func TestMissingPermissionsAreNamed(t *testing.T) {
	for queryType, permission := range map[string]string{
		"securitySummary":    "Security hub",
		"vulnerabilities":    "Security hub",
		"replications":       "Replication",
		"garbageCollections": "Garbage collection: List (system)",
		"jobQueues":          "Jobservice monitor: List (system)",
		"robots":             "Robot account: List (system)",
	} {
		r := query(t, limited(t), map[string]any{"queryType": queryType})
		if r.Error == nil || !strings.Contains(r.Error.Error(), permission) || r.Status != backend.StatusForbidden {
			t.Errorf("%s: %v (%v), want a refusal naming %q", queryType, r.Error, r.Status, permission)
		}
	}
	// What the limited account may read still works.
	f := frame(t, limited(t), map[string]any{"queryType": "artifacts", "projects": []string{"platform"}})
	if len(column(t, f, "tags")) != 2 {
		t.Errorf("limited artifacts = %v", column(t, f, "tags"))
	}
}

func TestResources(t *testing.T) {
	ds := reader(t)
	for path, want := range map[string]string{
		"projects":                      `{"name":"platform","public":false}`,
		"repositories":                  `"platform/alpine"`,
		"repositories?project=apps":     `["apps/nginx","apps/redis"]`,
		"policies":                      `["mirror-library"]`,
		"repositories?project=Not+Okay": `not a project name`,
	} {
		var body string
		err := ds.CallResource(context.Background(), &backend.CallResourceRequest{Method: "GET", Path: strings.SplitN(path, "?", 2)[0], URL: path},
			backend.CallResourceResponseSenderFunc(func(resp *backend.CallResourceResponse) error {
				body = string(resp.Body)
				return nil
			}))
		if err != nil || !strings.Contains(body, want) {
			t.Errorf("%s: %v %s, want %s", path, err, body, want)
		}
	}
}
