package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Sam-H101/grafana-harbor-datasource/pkg/harbor/harbortest"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

var ctx = context.Background()

func newDatasource(t *testing.T, url, jsonData string, secure map[string]string) *Datasource {
	t.Helper()
	if jsonData == "" {
		jsonData = `{}`
	}
	inst, err := NewDatasource(ctx, backend.DataSourceInstanceSettings{URL: url, JSONData: []byte(jsonData), DecryptedSecureJSONData: secure})
	if err != nil {
		t.Fatal(err)
	}
	return inst.(*Datasource)
}

func fakeDatasource(t *testing.T) (*Datasource, *harbortest.Fake) {
	t.Helper()
	f := harbortest.New(t)
	f.Username, f.Password = "robot$grafana", "pw"
	return newDatasource(t, f.URL, `{"username":"robot$grafana"}`, map[string]string{"password": "pw"}), f
}

func run(t *testing.T, d *Datasource, query string) backend.DataResponse {
	t.Helper()
	now := time.Date(2026, 11, 23, 4, 0, 0, 0, time.UTC)
	resp, err := d.QueryData(ctx, &backend.QueryDataRequest{Queries: []backend.DataQuery{{
		RefID: "A", JSON: json.RawMessage(query), TimeRange: backend.TimeRange{From: now.Add(-6 * time.Hour), To: now},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Responses["A"]
}

func frameOf(t *testing.T, d *Datasource, query string) *data.Frame {
	t.Helper()
	r := run(t, d, query)
	if r.Error != nil {
		t.Fatalf("%s: %v", query, r.Error)
	}
	if len(r.Frames) != 1 || r.Frames[0].RefID != "A" {
		t.Fatalf("%s: %d frames", query, len(r.Frames))
	}
	return r.Frames[0]
}

func column(t *testing.T, f *data.Frame, name string) *data.Field {
	t.Helper()
	field, _ := f.FieldByName(name)
	if field == nil {
		names := make([]string, len(f.Fields))
		for i, fl := range f.Fields {
			names[i] = fl.Name
		}
		t.Fatalf("no field %q in %v", name, names)
	}
	return field
}

// texts returns a column as text; a null is "".
func texts(t *testing.T, f *data.Frame, name string) []string {
	t.Helper()
	field := column(t, f, name)
	out := make([]string, field.Len())
	for i := range out {
		if v, ok := field.ConcreteAt(i); ok {
			out[i] = fmt.Sprint(v)
		}
	}
	return out
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

func TestProjects(t *testing.T) {
	d, f := fakeDatasource(t)
	frame := frameOf(t, d, `{"queryType":"projects"}`)
	if got := strings.Join(texts(t, frame, "project"), ","); got != "apps,library,mirror,platform,sandbox" {
		t.Fatalf("projects = %s", got)
	}
	if got := strings.Join(texts(t, frame, "public"), ","); got != "false,true,true,false,true" {
		t.Errorf("public = %s", got)
	}
	// platform has a quota of 500 MiB; apps has none.
	if quota := texts(t, frame, "storage_quota"); quota[3] != "524288000" || quota[0] != "" {
		t.Errorf("quotas = %v", quota)
	}
	if pct := texts(t, frame, "quota_used"); pct[3] == "" || pct[0] != "" {
		t.Errorf("quota used = %v", pct)
	}
	if used := column(t, frame, "storage_used"); used.Config.Unit != "bytes" || texts(t, frame, "storage_used")[0] == "" {
		t.Errorf("storage used: unit %q", used.Config.Unit)
	}
	if column(t, frame, "project").Config.DisplayName != "Project" {
		t.Error("the project column has no display name")
	}

	// The project list is cached for the queries of one refresh.
	frameOf(t, d, `{"queryType":"projects","projects":["sandbox"]}`)
	if n := len(f.Calls("projects")); n != 1 {
		t.Errorf("%d project listings, want 1", n)
	}

	// One number per project that has a quota, labelled for alert rules.
	frame = frameOf(t, d, `{"queryType":"projects","value":"quotaPercent"}`)
	if len(frame.Fields) != 3 || frame.Fields[1].Labels["project"] != "platform" || frame.Fields[1].Config.Unit != "percent" || frame.Fields[2].Labels["project"] != "sandbox" {
		t.Errorf("quota numbers: %d fields", len(frame.Fields))
	}
	if frame.Meta.Type != data.FrameTypeTimeSeriesWide {
		t.Errorf("frame type = %q", frame.Meta.Type)
	}
	frame = frameOf(t, d, `{"queryType":"projects","value":"repositories","projects":["apps"]}`)
	if len(frame.Fields) != 2 || frame.Fields[1].At(0).(float64) != 2 {
		t.Errorf("repositories of apps: %v", frame.Fields)
	}

	r := run(t, d, `{"queryType":"projects","projects":["nope"]}`)
	if r.Error == nil || r.Status != backend.StatusBadRequest || !strings.Contains(r.Error.Error(), `No such project "nope", or this login cannot see it.`) {
		t.Errorf("unknown project: %v (%v)", r.Error, r.Status)
	}
}

func TestProjectsWithoutQuotas(t *testing.T) {
	d, f := fakeDatasource(t)
	f.Forbidden["quotas"] = true
	frame := frameOf(t, d, `{"queryType":"projects"}`)
	if texts(t, frame, "storage_used")[0] != "" || !strings.Contains(notices(frame), "Quota: List (system)") {
		t.Errorf("used = %v, notice = %q", texts(t, frame, "storage_used"), notices(frame))
	}
	// The repository count does not need quotas; the storage does.
	frameOf(t, d, `{"queryType":"projects","value":"repositories"}`)
	r := run(t, d, `{"queryType":"projects","value":"storage"}`)
	if r.Error == nil || r.Status != backend.StatusForbidden || !strings.Contains(r.Error.Error(), "not allowed to read project quotas. Give the robot account the permission Quota: List (system)") {
		t.Errorf("storage without quotas: %v", r.Error)
	}
}

func TestRepositories(t *testing.T) {
	d, f := fakeDatasource(t)
	frame := frameOf(t, d, `{"queryType":"repositories"}`)
	if len(texts(t, frame, "full_name")) != 5 || len(f.Calls("repositories")) != 1 {
		t.Fatalf("repositories = %v", texts(t, frame, "full_name"))
	}

	// Several projects are asked one by one and sorted together.
	frame = frameOf(t, d, `{"queryType":"repositories","projects":["platform","apps"],"sort":"name","descending":true}`)
	if got := strings.Join(texts(t, frame, "full_name"), ","); got != "platform/alpine,apps/redis,apps/nginx" {
		t.Errorf("sorted = %s", got)
	}
	if got := strings.Join(texts(t, frame, "repository"), ","); got != "alpine,redis,nginx" {
		t.Errorf("names = %s", got)
	}
	if q := f.Calls("projects/apps/repositories")[0].Query; q.Get("sort") != "-name" {
		t.Errorf("query = %v", q)
	}

	frame = frameOf(t, d, `{"queryType":"repositories","projects":["apps","platform"],"sort":"artifacts","descending":true,"limit":1}`)
	if got := texts(t, frame, "full_name"); len(got) != 1 || got[0] != "platform/alpine" || !strings.Contains(notices(frame), "Showing the first 1 repositories") {
		t.Errorf("limit: %v, %q", got, notices(frame))
	}

	frame = frameOf(t, d, `{"queryType":"repositories","repository":"zzz"}`)
	if frame.Rows() != 0 || notices(frame) != "No matching repositories." {
		t.Errorf("no match: %d rows, %q", frame.Rows(), notices(frame))
	}
}

func TestArtifacts(t *testing.T) {
	d, f := fakeDatasource(t)
	frame := frameOf(t, d, `{"queryType":"artifacts","projects":["platform"],"repository":"alpine"}`)
	if frame.Fields[0].Name != "pushed" {
		t.Errorf("the first field is %q, want the push time for annotations", frame.Fields[0].Name)
	}
	// Newest push first.
	if got := strings.Join(texts(t, frame, "tags"), "|"); got != "3.20, stable|3.10" {
		t.Errorf("tags = %s", got)
	}
	if got := strings.Join(texts(t, frame, "severity"), ","); got != "High,Critical" {
		t.Errorf("severity = %s", got)
	}
	if got := strings.Join(texts(t, frame, "high"), ",") + ";" + strings.Join(texts(t, frame, "critical"), ","); got != "2,0;0,1" {
		t.Errorf("high;critical = %s", got)
	}
	if texts(t, frame, "platform")[0] != "linux/amd64" || texts(t, frame, "pulled")[0] == "" {
		t.Errorf("platform %v, pulled %v", texts(t, frame, "platform"), texts(t, frame, "pulled"))
	}

	// Without a repository, every repository of the projects is listed.
	frame = frameOf(t, d, `{"queryType":"artifacts","projects":["apps","platform"]}`)
	if got := strings.Join(texts(t, frame, "repository"), ","); got != "nginx,alpine,alpine" {
		t.Errorf("repositories = %s", got)
	}
	if len(f.Calls("projects/apps/repositories/redis/artifacts")) != 1 {
		t.Error("the artifacts of apps/redis were not asked for")
	}

	r := run(t, d, `{"queryType":"artifacts","projects":["apps"],"repository":"nope"}`)
	if r.Error == nil || !strings.Contains(r.Error.Error(), "There is no repository apps/nope.") {
		t.Errorf("unknown repository: %v", r.Error)
	}
	// An existing repository without artifacts is an empty table.
	frame = frameOf(t, d, `{"queryType":"artifacts","projects":["apps"],"repository":"redis"}`)
	if frame.Rows() != 0 || notices(frame) != "No matching artifacts." {
		t.Errorf("empty repository: %d rows, %q", frame.Rows(), notices(frame))
	}
}

func TestVulnerabilities(t *testing.T) {
	d, f := fakeDatasource(t)
	frame := frameOf(t, d, `{"queryType":"vulnerabilities"}`)
	if frame.Rows() != 5 {
		t.Fatalf("%d rows", frame.Rows())
	}
	if texts(t, frame, "project")[0] != "apps" || texts(t, frame, "repository")[0] != "nginx" || texts(t, frame, "tags")[0] != "1.19-alpine" {
		t.Errorf("where: %s/%s:%s", texts(t, frame, "project")[0], texts(t, frame, "repository")[0], texts(t, frame, "tags")[0])
	}
	if !strings.HasPrefix(texts(t, frame, "link")[0], "https://") || strings.Contains(texts(t, frame, "description")[0], "\n") || texts(t, frame, "cvss")[0] != "9.8" {
		t.Errorf("link %q, cvss %q", texts(t, frame, "link")[0], texts(t, frame, "cvss")[0])
	}
	if m := column(t, frame, "severity").Config.Mappings; len(m) != 1 {
		t.Error("severities are not coloured")
	}

	// Projects become project ids, one request each.
	f.Reset()
	frameOf(t, d, `{"queryType":"vulnerabilities","projects":["apps","platform"],"severity":"high","limit":3}`)
	calls := f.Calls("security/vul")
	if len(calls) != 2 {
		t.Fatalf("%d requests", len(calls))
	}
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.Query.Get("q")] = true
	}
	if !seen["severity=High,project_id=3"] || !seen["severity=High,project_id=2"] {
		t.Errorf("filters = %v", seen)
	}
}

func TestTotals(t *testing.T) {
	d, f := fakeDatasource(t)
	frame := frameOf(t, d, `{"queryType":"securitySummary"}`)
	if len(frame.Fields) != 10 || column(t, frame, "critical").At(0).(float64) != 10 || column(t, frame, "total").At(0).(float64) != 100 {
		t.Errorf("summary: %d fields", len(frame.Fields))
	}
	if c := column(t, frame, "scanned_artifacts"); c.Config.DisplayNameFromDS != "Scanned artifacts" || c.Labels["measure"] != "scanned_artifacts" {
		t.Errorf("scanned artifacts: %+v %v", c.Config, c.Labels)
	}
	frame = frameOf(t, d, `{"queryType":"securitySummary","measure":"high"}`)
	if len(frame.Fields) != 2 || frame.Fields[1].Name != "high" || frame.Fields[1].At(0).(float64) != 54 {
		t.Errorf("one number: %v", frame.Fields)
	}

	// The totals are added up from what the account can see, not taken from
	// Harbor's statistics, which give a robot account zeros.
	frame = frameOf(t, d, `{"queryType":"statistics"}`)
	for name, want := range map[string]float64{"projects": 5, "public_projects": 3, "private_projects": 2, "repositories": 5} {
		if got := column(t, frame, name).At(0).(float64); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if used := column(t, frame, "storage_used"); used.At(0).(float64) < 1e7 || used.Config.Unit != "bytes" {
		t.Errorf("storage used = %v", used.At(0))
	}
	if pct := column(t, frame, "disk_free_percent").At(0).(float64); pct <= 0 || pct >= 100 {
		t.Errorf("disk free = %v%%", pct)
	}

	f.Forbidden["quotas"], f.Forbidden["systeminfo/volumes"] = true, true
	frame = frameOf(t, d, `{"queryType":"statistics"}`)
	if len(frame.Fields) != 5 || !strings.Contains(notices(frame), "storage used (Quota: List (system)); the registry's disk (System volumes: Read (system))") {
		t.Errorf("%d fields, notice %q", len(frame.Fields), notices(frame))
	}
	frameOf(t, d, `{"queryType":"statistics","measure":"repositories"}`)
	r := run(t, d, `{"queryType":"statistics","measure":"disk_free"}`)
	if r.Error == nil || !strings.Contains(r.Error.Error(), "not allowed to read the registry's disk. Give the robot account the permission System volumes: Read (system)") {
		t.Errorf("disk without the permission: %v", r.Error)
	}
}

func TestHealth(t *testing.T) {
	d, f := fakeDatasource(t)
	frame := frameOf(t, d, `{"queryType":"health"}`)
	if frame.Rows() != 8 || texts(t, frame, "component")[0] != "core" || texts(t, frame, "status")[0] != "healthy" {
		t.Errorf("components = %v", texts(t, frame, "component"))
	}
	frame = frameOf(t, d, `{"queryType":"health","unhealthyOnly":true}`)
	if frame.Rows() != 0 || notices(frame) != "Every component is healthy." {
		t.Errorf("unhealthy: %d rows, %q", frame.Rows(), notices(frame))
	}

	f.Handlers["health"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"unhealthy","components":[{"name":"core","status":"healthy"},{"name":"registry","status":"unhealthy","error":"failed to check health:\n connection refused"}]}`))
	}
	frame = frameOf(t, d, `{"queryType":"health","unhealthyOnly":true}`)
	if got := texts(t, frame, "component"); len(got) != 1 || got[0] != "registry" || texts(t, frame, "error")[0] != "failed to check health: connection refused" {
		t.Errorf("unhealthy = %v, %v", got, texts(t, frame, "error"))
	}
	frame = frameOf(t, d, `{"queryType":"health","value":"status"}`)
	if len(frame.Fields) != 3 || frame.Fields[1].Labels["component"] != "core" || frame.Fields[1].At(0).(float64) != 1 || frame.Fields[2].At(0).(float64) != 0 {
		t.Errorf("numbers: %v", frame.Fields)
	}
}

func TestReplicationsAndGarbageCollection(t *testing.T) {
	d, f := fakeDatasource(t)
	frame := frameOf(t, d, `{"queryType":"replications","policy":"mirror-library"}`)
	if frame.Rows() != 1 || texts(t, frame, "rule")[0] != "mirror-library" || texts(t, frame, "status")[0] != "Succeed" || texts(t, frame, "succeeded")[0] != "1" {
		t.Errorf("run: %v %v", texts(t, frame, "rule"), texts(t, frame, "status"))
	}
	if d := column(t, frame, "duration"); d.Config.Unit != "s" || texts(t, frame, "duration")[0] == "" || frame.Fields[0].Name != "started" {
		t.Errorf("duration %v, first field %q", texts(t, frame, "duration"), frame.Fields[0].Name)
	}
	if q := f.Calls("replication/executions")[0].Query; q.Get("policy_id") != "1" {
		t.Errorf("query = %v", q)
	}
	frame = frameOf(t, d, `{"queryType":"replications","status":"failed"}`)
	if frame.Rows() != 0 || notices(frame) != "No matching replication runs." {
		t.Errorf("failed runs: %d, %q", frame.Rows(), notices(frame))
	}
	r := run(t, d, `{"queryType":"replications","policy":"nope"}`)
	if r.Error == nil || !strings.Contains(r.Error.Error(), `No such replication rule "nope".`) {
		t.Errorf("unknown rule: %v", r.Error)
	}

	frame = frameOf(t, d, `{"queryType":"garbageCollections"}`)
	if frame.Rows() != 1 || texts(t, frame, "status")[0] != "Success" || texts(t, frame, "trigger")[0] != "manual" || texts(t, frame, "dry_run")[0] != "true" || texts(t, frame, "freed_space")[0] != "0" {
		t.Errorf("gc: %v %v", texts(t, frame, "status"), texts(t, frame, "dry_run"))
	}
	if texts(t, frame, "duration")[0] == "" || column(t, frame, "freed_space").Config.Unit != "bytes" {
		t.Errorf("gc duration = %v", texts(t, frame, "duration"))
	}
}

func TestAuditLogQueuesAndRobots(t *testing.T) {
	d, f := fakeDatasource(t)
	frame := frameOf(t, d, `{"queryType":"auditLogs","user":"admin","limit":3}`)
	if frame.Rows() != 3 || frame.Fields[0].Name != "time" || texts(t, frame, "user")[0] != "admin" || texts(t, frame, "result")[0] != "Succeeded" {
		t.Errorf("audit log: %d rows, user %v", frame.Rows(), texts(t, frame, "user"))
	}
	if !strings.Contains(notices(frame), "Showing the first 3 audit log entries") {
		t.Errorf("notice = %q", notices(frame))
	}
	// The dashboard's time range goes to Harbor, in UTC.
	if q := f.Calls("auditlog-exts")[0].Query.Get("q"); q != "op_time=[2026-11-22 22:00:00~2026-11-23 04:00:00],username=admin" {
		t.Errorf("filter = %q", q)
	}

	frame = frameOf(t, d, `{"queryType":"jobQueues"}`)
	if frame.Rows() < 10 || texts(t, frame, "pending")[0] != "0" || texts(t, frame, "paused")[0] != "false" {
		t.Errorf("queues: %d rows", frame.Rows())
	}
	frame = frameOf(t, d, `{"queryType":"jobQueues","value":"latency"}`)
	if len(frame.Fields) < 10 || frame.Fields[1].Labels["job_type"] == "" || frame.Fields[1].Config.Unit != "s" {
		t.Errorf("queue numbers: %d fields", len(frame.Fields))
	}

	d.now = func() time.Time { return time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC) }
	f.Handlers["robots"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"robot$ci","level":"system","disable":true,"expires_at":1795478400},{"name":"robot$a","level":"system","expires_at":-1}]`))
	}
	frame = frameOf(t, d, `{"queryType":"robots"}`)
	if got := strings.Join(texts(t, frame, "name"), ","); got != "robot$a,robot$ci" {
		t.Errorf("robots = %s", got)
	}
	if got := strings.Join(texts(t, frame, "enabled"), ","); got != "true,false" {
		t.Errorf("enabled = %s", got)
	}
	// 1795478400 is 2026-11-24 00:00 UTC: one day left.
	if days := texts(t, frame, "days_left"); days[0] != "" || days[1] != "1" {
		t.Errorf("days left = %v", days)
	}
}

func TestMissingPermissionsAreNamed(t *testing.T) {
	d, f := fakeDatasource(t)
	for path, tc := range map[string]struct{ query, want string }{
		"security/summary":           {`{"queryType":"securitySummary"}`, "vulnerabilities. Give the robot account the permission Security hub: List and Read (system)"},
		"replication/policies":       {`{"queryType":"replications"}`, "replications. Give the robot account the permission Replication: List"},
		"system/gc":                  {`{"queryType":"garbageCollections"}`, "garbage collection runs. Give the robot account the permission Garbage collection: List (system)"},
		"jobservice/queues":          {`{"queryType":"jobQueues"}`, "job queues. Give the robot account the permission Jobservice monitor: List (system)"},
		"robots":                     {`{"queryType":"robots"}`, "robot accounts. Give the robot account the permission Robot account: List (system)"},
		"auditlog-exts":              {`{"queryType":"auditLogs"}`, "the audit log. Give the robot account the permission Audit log: List (system)"},
		"projects/apps/repositories": {`{"queryType":"artifacts","projects":["apps"]}`, "repositories. Give the robot account the permission Repository: List (project)"},
	} {
		f.Forbidden[path] = true
		r := run(t, d, tc.query)
		if r.Error == nil || r.Status != backend.StatusForbidden || r.ErrorSource != backend.ErrorSourceDownstream || !strings.Contains(r.Error.Error(), "The Harbor account is not allowed to read "+tc.want) {
			t.Errorf("%s: %v (%v, %v)", path, r.Error, r.Status, r.ErrorSource)
		}
	}
}

func TestQueryErrors(t *testing.T) {
	d, f := fakeDatasource(t)
	r := run(t, d, `{"queryType":"artifacts"}`)
	if r.Error == nil || r.Status != backend.StatusBadRequest || r.Error.Error() != "Choose at least one project to list artifacts of." {
		t.Errorf("invalid query: %v (%v)", r.Error, r.Status)
	}

	// Anonymous access: what needs a login says so.
	anon := newDatasource(t, f.URL, ``, nil)
	frame := frameOf(t, anon, `{"queryType":"projects"}`)
	if got := strings.Join(texts(t, frame, "project"), ","); got != "library,mirror,sandbox" {
		t.Errorf("anonymous projects = %s", got)
	}
	r = run(t, anon, `{"queryType":"robots"}`)
	if r.Error == nil || r.Status != backend.StatusUnauthorized || !strings.Contains(r.Error.Error(), "Harbor requires a login for this.") {
		t.Errorf("anonymous robots: %v (%v)", r.Error, r.Status)
	}

	f.Handlers["jobservice/queues"] = func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
	}
	slow := newDatasource(t, f.URL, `{"username":"robot$grafana","timeoutSeconds":1}`, map[string]string{"password": "pw"})
	r = run(t, slow, `{"queryType":"jobQueues"}`)
	if r.Error == nil || r.Status != backend.StatusTimeout || !strings.Contains(r.Error.Error(), "Harbor did not answer within 1 seconds.") {
		t.Errorf("timeout: %v (%v)", r.Error, r.Status)
	}

	bad := newDatasource(t, "", ``, nil)
	r = run(t, bad, `{}`)
	if r.Error == nil || !strings.Contains(r.Error.Error(), "Configuration problem: the Harbor URL is required") {
		t.Errorf("no URL: %v", r.Error)
	}
}

func TestQueriesRunSideBySide(t *testing.T) {
	d, _ := fakeDatasource(t)
	req := &backend.QueryDataRequest{}
	for i, q := range []string{`{"queryType":"projects"}`, `{"queryType":"health"}`, `{"queryType":"nope"}`, `{"queryType":"robots"}`} {
		req.Queries = append(req.Queries, backend.DataQuery{RefID: string(rune('A' + i)), JSON: json.RawMessage(q)})
	}
	resp, err := d.QueryData(ctx, req)
	if err != nil || len(resp.Responses) != 4 {
		t.Fatalf("%d responses, %v", len(resp.Responses), err)
	}
	if resp.Responses["A"].Error != nil || resp.Responses["B"].Error != nil || resp.Responses["D"].Error != nil || resp.Responses["C"].Error == nil {
		t.Errorf("errors: %v %v %v %v", resp.Responses["A"].Error, resp.Responses["B"].Error, resp.Responses["C"].Error, resp.Responses["D"].Error)
	}
}

func health(t *testing.T, d *Datasource) *backend.CheckHealthResult {
	t.Helper()
	res, err := d.CheckHealth(ctx, &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCheckHealth(t *testing.T) {
	d, f := fakeDatasource(t)
	res := health(t, d)
	if res.Status != backend.HealthStatusOk || !strings.HasPrefix(res.Message, "Connected to Harbor v2.15.") || !strings.HasSuffix(res.Message, "The account robot$grafana can see 5 projects.") {
		t.Errorf("ok: %v %q", res.Status, res.Message)
	}

	f.Forbidden["quotas"], f.Forbidden["robots"] = true, true
	res = health(t, d)
	if res.Status != backend.HealthStatusOk || !strings.HasSuffix(res.Message, "Not readable with this login: project quotas (Quota: List (system)); robot accounts (Robot account: List (system)).") {
		t.Errorf("missing permissions: %q", res.Message)
	}

	// A wrong password would otherwise pass for anonymous access.
	wrong := newDatasource(t, f.URL, `{"username":"robot$grafana"}`, map[string]string{"password": "nope"})
	res = health(t, wrong)
	if res.Status != backend.HealthStatusError || !strings.HasPrefix(res.Message, "Harbor rejected the user name or password.") {
		t.Errorf("wrong password: %v %q", res.Status, res.Message)
	}

	anon := newDatasource(t, f.URL, ``, nil)
	res = health(t, anon)
	if res.Status != backend.HealthStatusOk || !strings.Contains(res.Message, "Connected to Harbor. Anonymous access can see 3 projects. Without a login only public projects") {
		t.Errorf("anonymous: %v %q", res.Status, res.Message)
	}

	for name, tc := range map[string]struct {
		d    *Datasource
		want string
	}{
		"not harbor": {newDatasource(t, f.URL+"/c", ``, nil), "did not answer like Harbor. Check the URL"},
		"no url":     {newDatasource(t, "", ``, nil), "Configuration problem: the Harbor URL is required"},
		"refused":    {newDatasource(t, "http://127.0.0.1:1", ``, nil), "Cannot reach Harbor at http://127.0.0.1:1: connection refused. If Harbor is on a private network"},
	} {
		res := health(t, tc.d)
		if res.Status != backend.HealthStatusError || !strings.Contains(res.Message, tc.want) {
			t.Errorf("%s: %v %q", name, res.Status, res.Message)
		}
	}

	// A proxy in front of Harbor that wants its own login.
	f.Handlers["systeminfo"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }
	res = health(t, d)
	if res.Status != backend.HealthStatusError || !strings.Contains(res.Message, "asked for a login before answering Harbor's system information") {
		t.Errorf("proxy login: %q", res.Message)
	}
}

type recorder struct {
	status int
	body   string
}

func (r *recorder) Send(resp *backend.CallResourceResponse) error {
	r.status, r.body = resp.Status, string(resp.Body)
	return nil
}

func resource(t *testing.T, d *Datasource, path string) *recorder {
	t.Helper()
	rec := &recorder{}
	base, _, _ := strings.Cut(path, "?")
	if err := d.CallResource(ctx, &backend.CallResourceRequest{Method: http.MethodGet, Path: base, URL: path}, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestResources(t *testing.T) {
	d, f := fakeDatasource(t)
	rec := resource(t, d, "projects")
	if rec.status != 200 || !strings.HasPrefix(rec.body, `[{"name":"apps","public":false},{"name":"library","public":true}`) {
		t.Errorf("projects: %d %s", rec.status, rec.body)
	}
	rec = resource(t, d, "repositories?project=apps")
	if rec.status != 200 || strings.TrimSpace(rec.body) != `["apps/nginx","apps/redis"]` {
		t.Errorf("repositories of apps: %d %s", rec.status, rec.body)
	}
	rec = resource(t, d, "repositories")
	if rec.status != 200 || !strings.Contains(rec.body, `"platform/alpine"`) || !strings.Contains(rec.body, `"mirror/busybox"`) {
		t.Errorf("repositories: %d %s", rec.status, rec.body)
	}
	rec = resource(t, d, "policies")
	if rec.status != 200 || strings.TrimSpace(rec.body) != `["mirror-library"]` {
		t.Errorf("policies: %d %s", rec.status, rec.body)
	}

	rec = resource(t, d, "repositories?project=Not+Okay")
	if rec.status != 400 || !strings.Contains(rec.body, "is not a project name") {
		t.Errorf("bad project: %d %s", rec.status, rec.body)
	}
	f.Forbidden["replication/policies"] = true
	rec = resource(t, d, "policies")
	if rec.status != 403 || !strings.Contains(rec.body, "Replication policy: List") {
		t.Errorf("forbidden policies: %d %s", rec.status, rec.body)
	}
	if rec := resource(t, newDatasource(t, "", ``, nil), "projects"); rec.status != 400 || !strings.Contains(rec.body, "Harbor URL is required") {
		t.Errorf("no settings: %d %s", rec.status, rec.body)
	}
}
