package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func settings(url, jsonData string, secure map[string]string) backend.DataSourceInstanceSettings {
	return backend.DataSourceInstanceSettings{URL: url, JSONData: []byte(jsonData), DecryptedSecureJSONData: secure}
}

func TestLoadSettings(t *testing.T) {
	s, err := LoadSettings(settings(" https://harbor.example.com/ ", `{"username":" robot$grafana ","timeoutSeconds":45}`, map[string]string{"password": " p w "}))
	if err != nil {
		t.Fatal(err)
	}
	if s.URL != "https://harbor.example.com" || s.Username != "robot$grafana" || s.Password != " p w " || s.Timeout != 45*time.Second {
		t.Errorf("settings = %+v", s)
	}

	// Anonymous, with the default timeout.
	s, err = LoadSettings(settings("http://harbor:8080", ``, nil))
	if err != nil || s.Username != "" || s.Timeout != DefaultTimeoutSeconds*time.Second {
		t.Errorf("anonymous: %+v, %v", s, err)
	}
}

func TestURLIsReducedToTheBase(t *testing.T) {
	for in, want := range map[string]string{
		"http://harbor:8080/":                                "http://harbor:8080",
		"https://host/harbor/projects":                       "https://host",
		"https://host/harbor/projects/2/repositories/alpine": "https://host",
		"https://host/harbor":                                "https://host",
		"https://host/api/v2.0":                              "https://host",
		"https://host/api/v2.0/projects":                     "https://host",
		"https://host/account/sign-in?":                      "https://host",
		"https://host/devcenter-api-2.0":                     "https://host",
		"HTTPS://Host.Example.com:8443///":                   "https://Host.Example.com:8443",
		"https://host/registry/harbor/projects":              "https://host/registry",
		"https://host/#anything":                             "https://host",
	} {
		s, err := LoadSettings(settings(in, ``, nil))
		if err != nil || s.URL != want {
			t.Errorf("%q: got %v, %v; want %q", in, s, err, want)
		}
	}
}

func TestInvalidSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		url, json string
		secure    map[string]string
		want      string
	}{
		"no url":            {"", ``, nil, "URL is required"},
		"no scheme":         {"harbor.example.com", ``, nil, "http:// or https://"},
		"ftp":               {"ftp://harbor", ``, nil, "http:// or https://"},
		"credentials":       {"https://admin:secret@harbor", ``, nil, "their own fields"},
		"query":             {"https://harbor/?a=b", ``, nil, "query string"},
		"password only":     {"https://harbor", ``, map[string]string{"password": "x"}, "account name too"},
		"user only":         {"https://harbor", `{"username":"robot$grafana"}`, nil, `enter the password or secret of "robot$grafana"`},
		"colon in the user": {"https://harbor", `{"username":"a:b"}`, map[string]string{"password": "x"}, "colon"},
		"timeout":           {"https://harbor", `{"timeoutSeconds":301}`, nil, "between 1 and 300"},
		"bad json":          {"https://harbor", `{`, nil, "parse settings"},
	} {
		_, err := LoadSettings(settings(tc.url, tc.json, tc.secure))
		if !errors.Is(err, ErrInvalidSettings) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "secret@") {
			t.Errorf("%s: the message leaks credentials: %v", name, err)
		}
	}
}

func TestParseQueryDefaults(t *testing.T) {
	qm, err := ParseQuery(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if qm.QueryType != QueryProjects || qm.Limit != DefaultLimit || qm.Value != "" || len(qm.Projects) != 0 {
		t.Errorf("defaults = %+v", qm)
	}
}

func TestParseQuery(t *testing.T) {
	qm, err := ParseQuery(json.RawMessage(`{
		"queryType":"vulnerabilities",
		"projects":["apps, platform","apps"," "],
		"repository":" apps/team/api ",
		"severity":"critical",
		"cve":"cve-2021-3711",
		"package":"libcrypto1.1",
		"limit":25
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(qm.Projects, "|"); got != "apps|platform" {
		t.Errorf("projects = %s", got)
	}
	if qm.Repository != "apps/team/api" || qm.Severity != "Critical" || qm.CVE != "CVE-2021-3711" || qm.Package != "libcrypto1.1" || qm.Limit != 25 {
		t.Errorf("query = %+v", qm)
	}

	// Artifacts take the repository inside the project, with or without the project in front.
	for _, repo := range []string{"alpine", "platform/alpine"} {
		qm, err = ParseQuery(json.RawMessage(fmt.Sprintf(`{"queryType":"artifacts","projects":["platform"],"repository":%q}`, repo)))
		if err != nil || qm.Repository != "alpine" {
			t.Errorf("%s: %+v, %v", repo, qm, err)
		}
	}

	qm, err = ParseQuery(json.RawMessage(`{"queryType":"replications","policy":"mirror library","status":"InProgress"}`))
	if err != nil || qm.Policy != "mirror library" || qm.Status != "InProgress" {
		t.Errorf("replications: %+v, %v", qm, err)
	}

	// A robot account's name has a dollar sign and is not a variable.
	qm, err = ParseQuery(json.RawMessage(`{"queryType":"auditLogs","user":"robot$grafana","operation":"create","resourceType":"robot"}`))
	if err != nil || qm.User != "robot$grafana" || qm.Operation != "create" || qm.ResourceType != "robot" {
		t.Errorf("audit log: %+v, %v", qm, err)
	}

	for _, ok := range []string{
		`{"queryType":"projects","value":"table"}`,
		`{"queryType":"projects","value":"quotaPercent"}`,
		`{"queryType":"jobQueues","value":"latency"}`,
		`{"queryType":"health","value":"status","unhealthyOnly":true}`,
		`{"queryType":"securitySummary","measure":"critical"}`,
		`{"queryType":"statistics","measure":"disk_free_percent"}`,
		`{"queryType":"repositories","sort":"pulls","descending":true,"limit":10000}`,
		`{"queryType":"vulnerabilities","severity":""}`,
	} {
		if _, err := ParseQuery(json.RawMessage(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestInvalidQueries(t *testing.T) {
	many := make([]string, MaxProjects+1)
	for i := range many {
		many[i] = fmt.Sprintf("p%d", i)
	}
	tooMany, _ := json.Marshal(map[string]any{"projects": many})

	for name, tc := range map[string]struct{ query, want string }{
		"not json":              {`{`, "not valid JSON"},
		"unknown type":          {`{"queryType":"blobs"}`, `unknown query type "blobs"`},
		"too many projects":     {string(tooMany), "too many projects"},
		"bad project":           {`{"projects":["My Project"]}`, "is not a project name"},
		"project variable":      {`{"projects":["$project"]}`, "dashboard variable without a value"},
		"braced variable":       {`{"projects":["${project:csv}"]}`, "dashboard variable without a value"},
		"artifacts, no project": {`{"queryType":"artifacts"}`, "choose at least one project"},
		"repository, two projects": {`{"queryType":"artifacts","projects":["a","b"],"repository":"x"}`,
			"only when one project is chosen"},
		"bad repository":      {`{"queryType":"artifacts","projects":["a"],"repository":"Not A Repo"}`, "is not a repository name"},
		"bad full name":       {`{"queryType":"vulnerabilities","repository":"UPPER/case"}`, "use the full name"},
		"repository variable": {`{"queryType":"repositories","repository":"$repo"}`, "dashboard variable without a value"},
		"filter characters":   {`{"queryType":"vulnerabilities","package":"a,b=c"}`, "characters Harbor's filters cannot take"},
		"long text":           {`{"queryType":"auditLogs","user":"` + strings.Repeat("a", 301) + `"}`, "longer than 300"},
		"severity":            {`{"queryType":"vulnerabilities","severity":"severe"}`, "the severity must be"},
		"severity variable":   {`{"queryType":"vulnerabilities","severity":"$severity"}`, "dashboard variable without a value"},
		"status":              {`{"queryType":"replications","status":"done"}`, "the status must be"},
		"sort":                {`{"queryType":"repositories","sort":"size"}`, "can be sorted by"},
		"limit too low":       {`{"limit":-1}`, "between 1 and 10000"},
		"limit too high":      {`{"limit":10001}`, "between 1 and 10000"},
		"project value":       {`{"queryType":"projects","value":"pending"}`, "projects can be shown as"},
		"queue value":         {`{"queryType":"jobQueues","value":"storage"}`, "job queues can be shown as"},
		"health value":        {`{"queryType":"health","value":"storage"}`, "health can be shown as"},
		"summary measure":     {`{"queryType":"securitySummary","measure":"projects"}`, `no number "projects"`},
		"statistics measure":  {`{"queryType":"statistics","measure":"critical"}`, `no number "critical"`},
	} {
		_, err := ParseQuery(json.RawMessage(tc.query))
		if !errors.Is(err, ErrInvalidQuery) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}
