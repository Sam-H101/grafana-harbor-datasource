package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// QueryType selects what a query returns.
type QueryType string

const (
	// QueryProjects returns the projects with their storage use and quota.
	QueryProjects QueryType = "projects"
	// QueryRepositories returns repositories with artifact and pull counts.
	QueryRepositories QueryType = "repositories"
	// QueryArtifacts returns the artifacts (images) of repositories, with
	// tags, size, push and pull times and the result of the last scan.
	QueryArtifacts QueryType = "artifacts"
	// QueryVulnerabilities returns findings from Harbor's security hub.
	QueryVulnerabilities QueryType = "vulnerabilities"
	// QuerySecuritySummary returns vulnerability totals by severity.
	QuerySecuritySummary QueryType = "securitySummary"
	// QueryStatistics returns system totals: projects, repositories,
	// storage used and the registry's disk.
	QueryStatistics QueryType = "statistics"
	// QueryHealth returns the health of Harbor's components.
	QueryHealth QueryType = "health"
	// QueryReplications returns runs of replication rules.
	QueryReplications QueryType = "replications"
	// QueryGarbageCollections returns runs of garbage collection.
	QueryGarbageCollections QueryType = "garbageCollections"
	// QueryAuditLogs returns the audit log of the dashboard's time range.
	QueryAuditLogs QueryType = "auditLogs"
	// QueryJobQueues returns the background job queues.
	QueryJobQueues QueryType = "jobQueues"
	// QueryRobots returns the robot accounts and when they expire.
	QueryRobots QueryType = "robots"
)

// Limits on what one query may ask for. They bound the number of requests a
// single panel can cause.
const (
	MaxProjects   = 100
	DefaultLimit  = 100
	MaxLimit      = 10000
	maxTextLength = 300
	// MaxRepositoriesPerQuery bounds how many repositories one artifacts
	// query lists the artifacts of.
	MaxRepositoriesPerQuery = 200
)

// QueryModel is a validated query. Fields that do not apply to the query
// type are ignored.
type QueryModel struct {
	QueryType QueryType
	// Projects restricts projects, repositories, artifacts and
	// vulnerabilities. Empty means every project, except for artifacts,
	// which need at least one.
	Projects []string
	// Repository is, for repositories, text the name must contain; for
	// artifacts, a repository name inside the chosen project (empty: every
	// repository of the projects); for vulnerabilities, a full name
	// "project/repository".
	Repository string
	// Severity, CVE and Package narrow vulnerabilities (exact matches).
	Severity string
	CVE      string
	Package  string
	// Status and Policy narrow replication runs.
	Status string
	Policy string
	// User, Operation and ResourceType narrow the audit log (exact matches).
	User         string
	Operation    string
	ResourceType string
	// Sort orders repositories: "", name, pulls, artifacts or updated.
	Sort       string
	Descending bool
	Limit      int
	// UnhealthyOnly keeps only components that are not healthy.
	UnhealthyOnly bool
	// Value turns a table into one number per row, for alert rules and stat
	// panels. Projects: storage, quotaPercent or repositories. Job queues:
	// pending or latency. Health: status (1 healthy, 0 not). Empty means
	// the table.
	Value string
	// Measure keeps one number of the vulnerability totals or the system
	// totals, for an alert rule on it. Empty means all of them.
	Measure string
}

type rawQuery struct {
	QueryType     string   `json:"queryType"`
	Projects      []string `json:"projects"`
	Repository    string   `json:"repository"`
	Severity      string   `json:"severity"`
	CVE           string   `json:"cve"`
	Package       string   `json:"package"`
	Status        string   `json:"status"`
	Policy        string   `json:"policy"`
	User          string   `json:"user"`
	Operation     string   `json:"operation"`
	ResourceType  string   `json:"resourceType"`
	Sort          string   `json:"sort"`
	Descending    bool     `json:"descending"`
	Limit         int      `json:"limit"`
	UnhealthyOnly bool     `json:"unhealthyOnly"`
	Value         string   `json:"value"`
	Measure       string   `json:"measure"`
}

// ErrInvalidQuery wraps every query validation failure.
var ErrInvalidQuery = errors.New("invalid query")

func badQuery(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, fmt.Sprintf(format, a...))
}

var (
	// Harbor project names: lower-case letters, digits, and dots,
	// underscores or hyphens between them.
	projectPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-]+[a-z0-9]+)*$`)
	// A repository name is a path of such components.
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-]+[a-z0-9]+)*(?:/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)*$`)
	// Text that goes into Harbor's q parameter: nothing that q gives a
	// meaning to (comma, braces, brackets, tilde, equals, quotes).
	filterPattern = regexp.MustCompile(`^[A-Za-z0-9 ._:@/$+\-]*$`)
	// A dashboard variable that Grafana did not replace ($project,
	// ${project}, [[project]]): the variable does not exist or has no value.
	// A $ inside a word is left alone: robot accounts are named robot$name.
	variablePattern = regexp.MustCompile(`(?:^|\W)\$[A-Za-z_]\w*|\$\{[A-Za-z_]\w*|\[\[\w+`)
)

var (
	// Severities as Harbor spells them.
	severities = map[string]string{
		"critical": "Critical", "high": "High", "medium": "Medium", "low": "Low",
		"negligible": "Negligible", "none": "None", "unknown": "Unknown",
	}
	// Replication run states as Harbor spells them.
	replicationStatuses = map[string]string{
		"succeed": "Succeed", "failed": "Failed", "inprogress": "InProgress", "stopped": "Stopped",
	}
	repositorySorts = set("name", "pulls", "artifacts", "updated")
	projectValues   = set("storage", "quotaPercent", "repositories")
	queueValues     = set("pending", "latency")
	healthValues    = set("status")
	// The numbers the two totals queries return.
	summaryMeasures    = set("critical", "high", "medium", "low", "unknown", "total", "fixable", "scanned_artifacts", "artifacts")
	statisticsMeasures = set("projects", "public_projects", "private_projects", "repositories", "storage_used", "disk_total", "disk_free", "disk_free_percent")
	queryTypes         = set(
		string(QueryProjects), string(QueryRepositories), string(QueryArtifacts), string(QueryVulnerabilities),
		string(QuerySecuritySummary), string(QueryStatistics), string(QueryHealth), string(QueryReplications),
		string(QueryGarbageCollections), string(QueryAuditLogs), string(QueryJobQueues), string(QueryRobots),
	)
)

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// ParseQuery validates the JSON of one query.
func ParseQuery(raw json.RawMessage) (*QueryModel, error) {
	var rq rawQuery
	if err := json.Unmarshal(raw, &rq); err != nil {
		return nil, badQuery("the query is not valid JSON")
	}
	if rq.QueryType == "" {
		rq.QueryType = string(QueryProjects)
	}
	if !queryTypes[rq.QueryType] {
		return nil, badQuery("unknown query type %q", truncate(rq.QueryType, 40))
	}
	qm := &QueryModel{
		QueryType:     QueryType(rq.QueryType),
		Descending:    rq.Descending,
		UnhealthyOnly: rq.UnhealthyOnly,
	}

	var err error
	if qm.Projects, err = cleanList(rq.Projects, "projects", MaxProjects); err != nil {
		return nil, err
	}
	for _, p := range qm.Projects {
		if variablePattern.MatchString(p) {
			return nil, badQuery("the project %q is a dashboard variable without a value", truncate(p, 60))
		}
		if len(p) > 255 || !projectPattern.MatchString(p) {
			return nil, badQuery("%q is not a project name; names have lower-case letters, digits, dots, hyphens and underscores", truncate(p, 60))
		}
	}
	if qm.QueryType == QueryArtifacts && len(qm.Projects) == 0 {
		return nil, badQuery("choose at least one project to list artifacts of")
	}

	// Free text that ends up in Harbor's filter expression.
	for _, t := range []struct {
		what string
		in   string
		out  *string
	}{
		{"repository", rq.Repository, &qm.Repository},
		{"CVE", rq.CVE, &qm.CVE},
		{"package", rq.Package, &qm.Package},
		{"replication rule", rq.Policy, &qm.Policy},
		{"user", rq.User, &qm.User},
		{"operation", rq.Operation, &qm.Operation},
		{"resource type", rq.ResourceType, &qm.ResourceType},
	} {
		v := strings.TrimSpace(t.in)
		if len(v) > maxTextLength {
			return nil, badQuery("the %s is longer than %d characters", t.what, maxTextLength)
		}
		if variablePattern.MatchString(v) {
			return nil, badQuery("the %s %q is a dashboard variable without a value", t.what, truncate(v, 60))
		}
		if !filterPattern.MatchString(v) {
			return nil, badQuery("the %s %q has characters Harbor's filters cannot take; use letters, digits, spaces and . _ - : / @ $ +", t.what, truncate(v, 60))
		}
		*t.out = v
	}
	qm.CVE = strings.ToUpper(qm.CVE)

	switch qm.QueryType {
	case QueryArtifacts:
		if qm.Repository != "" {
			if !repositoryPattern.MatchString(qm.Repository) {
				return nil, badQuery("%q is not a repository name", truncate(qm.Repository, 60))
			}
			if len(qm.Projects) > 1 {
				return nil, badQuery("a repository can be named only when one project is chosen")
			}
			// Accept the full name "project/repository" too.
			qm.Repository = strings.TrimPrefix(qm.Repository, qm.Projects[0]+"/")
		}
	case QueryVulnerabilities:
		if qm.Repository != "" && !repositoryPattern.MatchString(qm.Repository) {
			return nil, badQuery("%q is not a repository name; use the full name, such as library/nginx", truncate(qm.Repository, 60))
		}
	}

	if v := strings.ToLower(strings.TrimSpace(rq.Severity)); v != "" {
		if variablePattern.MatchString(rq.Severity) {
			return nil, badQuery("the severity %q is a dashboard variable without a value", truncate(rq.Severity, 60))
		}
		name, ok := severities[v]
		if !ok {
			return nil, badQuery("the severity must be Critical, High, Medium, Low, Negligible, None or Unknown")
		}
		qm.Severity = name
	}

	if v := strings.ToLower(strings.TrimSpace(rq.Status)); v != "" {
		name, ok := replicationStatuses[v]
		if !ok {
			return nil, badQuery("the status must be Succeed, Failed, InProgress or Stopped")
		}
		qm.Status = name
	}

	qm.Sort = strings.TrimSpace(rq.Sort)
	if qm.Sort != "" && !repositorySorts[qm.Sort] {
		return nil, badQuery("repositories can be sorted by name, pulls, artifacts or updated")
	}

	switch {
	case rq.Limit == 0:
		qm.Limit = DefaultLimit
	case rq.Limit < 0 || rq.Limit > MaxLimit:
		return nil, badQuery("the limit must be between 1 and %d", MaxLimit)
	default:
		qm.Limit = rq.Limit
	}

	qm.Value = strings.TrimSpace(rq.Value)
	if qm.Value == "table" {
		qm.Value = ""
	}
	switch {
	case qm.Value == "":
	case qm.QueryType == QueryProjects && !projectValues[qm.Value]:
		return nil, badQuery("projects can be shown as a table, or as their storage used, quota used in percent or repository count")
	case qm.QueryType == QueryJobQueues && !queueValues[qm.Value]:
		return nil, badQuery("job queues can be shown as a table, or as their pending jobs or latency")
	case qm.QueryType == QueryHealth && !healthValues[qm.Value]:
		return nil, badQuery("health can be shown as a table, or as one number per component: 1 for healthy, 0 for not")
	}

	qm.Measure = strings.TrimSpace(rq.Measure)
	switch {
	case qm.Measure == "":
	case qm.QueryType == QuerySecuritySummary && !summaryMeasures[qm.Measure]:
		return nil, badQuery("the vulnerability totals have no number %q", truncate(qm.Measure, 40))
	case qm.QueryType == QueryStatistics && !statisticsMeasures[qm.Measure]:
		return nil, badQuery("the totals have no number %q", truncate(qm.Measure, 40))
	}
	return qm, nil
}

// cleanList trims, drops blanks and duplicates, and enforces a maximum. One
// entry may hold several comma-separated values, which is how Grafana expands
// a multi-value variable.
func cleanList(in []string, what string, limit int) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, entry := range in {
		for _, v := range strings.Split(entry, ",") {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) > limit {
		return nil, badQuery("too many %s: %d selected, the limit is %d", what, len(out), limit)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
