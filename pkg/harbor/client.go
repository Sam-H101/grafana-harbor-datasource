// Package harbor is a client for the REST API (v2.0) of the Harbor container
// registry. It only reads. docs/harbor-api-notes.md records the observed
// behaviour the client relies on.
package harbor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// maxResponseBytes caps one response body.
	maxResponseBytes = 32 << 20
	userAgent        = "grafana-harbor-datasource"
	// pageSize is the largest page Harbor serves.
	pageSize = 100
	// maxPages bounds how many pages one listing may follow.
	maxPages = 200
	apiPath  = "api/v2.0/"
)

var (
	// ErrUnauthorized is a 401: the credentials are missing or wrong.
	ErrUnauthorized = errors.New("harbor: unauthorized")
	// ErrForbidden is a 403: the account lacks a permission. Harbor also
	// answers 403 for a project that does not exist.
	ErrForbidden = errors.New("harbor: forbidden")
	// ErrNotHarbor means the server answered, but not like the Harbor API:
	// usually a wrong URL or a login page.
	ErrNotHarbor = errors.New("harbor: unexpected response")
	// ErrResponseTooLarge means a response exceeded maxResponseBytes.
	ErrResponseTooLarge = errors.New("harbor: response too large")
)

// APIError is an error reported by the API.
type APIError struct {
	Status int
	// Code is Harbor's error code, such as NOT_FOUND or BAD_REQUEST.
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Message }

// NotFound reports whether the API said the thing asked for does not exist.
func (e *APIError) NotFound() bool { return e.Status == http.StatusNotFound }

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.NotFound()
}

// Config is what the client needs to reach one Harbor.
type Config struct {
	// BaseURL has no trailing slash and no /api/v2.0 suffix.
	BaseURL  string
	Username string
	Password string
}

// Client talks to one Harbor. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
	auth string
}

// NewClient returns a client that sends its requests through hc.
func NewClient(cfg Config, hc *http.Client) (*Client, error) {
	if u, err := url.Parse(cfg.BaseURL); err != nil || cfg.BaseURL == "" || u.Host == "" {
		return nil, errors.New("invalid base URL")
	}
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	c := &Client{cfg: cfg, http: hc}
	if cfg.Username != "" {
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.Username+":"+cfg.Password))
	}
	return c, nil
}

// HasCredentials reports whether requests are authenticated.
func (c *Client) HasCredentials() bool { return c.auth != "" }

// get calls one endpoint under /api/v2.0/. It decodes a JSON answer into out
// (unless out is nil) and returns the response headers.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) (http.Header, error) {
	u := c.cfg.BaseURL + "/" + apiPath + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Drain a little so the connection can be reused, then close.
		_, _ = io.CopyN(io.Discard, resp.Body, 4096)
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, ErrResponseTooLarge
	}

	if resp.StatusCode >= 400 {
		code, msg, ok := errorMessage(body)
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, ErrUnauthorized
		case resp.StatusCode == http.StatusForbidden:
			return nil, ErrForbidden
		case !ok && resp.StatusCode == http.StatusNotFound:
			// Harbor's own 404 is JSON; anything else is another server.
			return nil, fmt.Errorf("%w: %s was not found", ErrNotHarbor, path)
		case !ok:
			msg = fmt.Sprintf("Harbor answered HTTP %d", resp.StatusCode)
		}
		return nil, &APIError{Status: resp.StatusCode, Code: code, Message: msg}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("%w: the answer to %s is not JSON", ErrNotHarbor, path)
		}
	}
	return resp.Header, nil
}

// errorMessage reads the shape Harbor reports errors in:
//
//	{"errors":[{"code":"NOT_FOUND","message":"path /api/v2.0/x was not found"}]}
//
// The last result is false when the body is not such an error.
func errorMessage(body []byte) (code, message string, ok bool) {
	var e struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Errors) == 0 {
		return "", "", false
	}
	msgs := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		if item.Message != "" {
			msgs = append(msgs, item.Message)
		}
	}
	msg := strings.Join(strings.Fields(strings.Join(msgs, "; ")), " ")
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	if msg == "" {
		msg = strings.ToLower(strings.ReplaceAll(e.Errors[0].Code, "_", " "))
	}
	return e.Errors[0].Code, msg, true
}

// list follows the pages of a listing until it has limit items (0 means all)
// or the listing ends. The second result says whether more items exist than
// were returned.
func list[T any](ctx context.Context, c *Client, path string, params url.Values, limit int) ([]T, bool, error) {
	if params == nil {
		params = url.Values{}
	}
	size := pageSize
	if limit > 0 && limit < size {
		size = limit
	}
	params.Set("page_size", strconv.Itoa(size))
	var all []T
	for page := 1; page <= maxPages; page++ {
		params.Set("page", strconv.Itoa(page))
		var items []T
		header, err := c.get(ctx, path, params, &items)
		if err != nil {
			return nil, false, err
		}
		all = append(all, items...)
		// X-Total-Count is -1 where Harbor does not count (tune_count).
		total, terr := strconv.Atoi(header.Get("X-Total-Count"))
		known := terr == nil && total >= 0
		if limit > 0 && len(all) >= limit {
			more := len(all) > limit || (known && total > limit) || (!known && len(items) == size)
			return all[:limit], more, nil
		}
		if len(items) < size || (known && len(all) >= total) {
			return all, false, nil
		}
	}
	return all, true, nil
}

// Time is a Harbor timestamp. Harbor writes year 1 for "never".
type Time struct{ time.Time }

// UnmarshalJSON accepts RFC 3339 and treats null, "" and year 1 as unset.
func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	if parsed.Year() <= 1 {
		parsed = time.Time{}
	}
	t.Time = parsed
	return nil
}

// Ptr returns nil for an unset time.
func (t Time) Ptr() *time.Time {
	if t.IsZero() {
		return nil
	}
	v := t.UTC()
	return &v
}

// SystemInfo is what Harbor says about itself.
type SystemInfo struct {
	// Version is empty for a request that is not logged in.
	Version     string `json:"harbor_version"`
	AuthMode    string `json:"auth_mode"`
	ReadOnly    bool   `json:"read_only"`
	ExternalURL string `json:"external_url"`
}

// SystemInfo answers without a login too, but then without the version.
func (c *Client) SystemInfo(ctx context.Context) (*SystemInfo, error) {
	var raw map[string]json.RawMessage
	if _, err := c.get(ctx, "systeminfo", nil, &raw); err != nil {
		return nil, err
	}
	if _, ok := raw["auth_mode"]; !ok {
		return nil, fmt.Errorf("%w: systeminfo has no auth_mode", ErrNotHarbor)
	}
	info := &SystemInfo{}
	_ = json.Unmarshal(raw["harbor_version"], &info.Version)
	_ = json.Unmarshal(raw["auth_mode"], &info.AuthMode)
	_ = json.Unmarshal(raw["read_only"], &info.ReadOnly)
	_ = json.Unmarshal(raw["external_url"], &info.ExternalURL)
	return info, nil
}

// Component is one part of Harbor in its health report.
type Component struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// Health is Harbor's own health report. It needs no login.
type Health struct {
	Status     string      `json:"status"`
	Components []Component `json:"components"`
}

func (c *Client) Health(ctx context.Context) (*Health, error) {
	var h Health
	if _, err := c.get(ctx, "health", nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// Statistics are Harbor's totals. Only a system administrator gets the
// overall totals; other accounts get zeros there.
type Statistics struct {
	PrivateProjects int64 `json:"private_project_count"`
	PublicProjects  int64 `json:"public_project_count"`
	TotalProjects   int64 `json:"total_project_count"`
	PrivateRepos    int64 `json:"private_repo_count"`
	PublicRepos     int64 `json:"public_repo_count"`
	TotalRepos      int64 `json:"total_repo_count"`
	TotalStorage    int64 `json:"total_storage_consumption"`
}

// Statistics needs a login and nothing else, which makes it the request that
// tells whether the credentials are accepted.
func (c *Client) Statistics(ctx context.Context) (*Statistics, error) {
	var s Statistics
	if _, err := c.get(ctx, "statistics", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Volume is the disk Harbor's registry stores its data on.
type Volume struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
}

func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	var v struct {
		Storage []Volume `json:"storage"`
	}
	if _, err := c.get(ctx, "systeminfo/volumes", nil, &v); err != nil {
		return nil, err
	}
	return v.Storage, nil
}

// Project is a Harbor project.
type Project struct {
	ID        int64  `json:"project_id"`
	Name      string `json:"name"`
	OwnerName string `json:"owner_name"`
	RepoCount int64  `json:"repo_count"`
	Created   Time   `json:"creation_time"`
	Metadata  struct {
		Public   string `json:"public"`
		AutoScan string `json:"auto_scan"`
	} `json:"metadata"`
	// RegistryID is set for a proxy cache project.
	RegistryID int64 `json:"registry_id"`
}

// Public reports whether anyone may pull from the project.
func (p Project) Public() bool { return p.Metadata.Public == "true" }

// Projects lists every project the account can see.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	items, _, err := list[Project](ctx, c, "projects", url.Values{"with_detail": {"true"}}, 0)
	return items, err
}

// Quota is the storage quota of one project.
type Quota struct {
	Ref struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"ref"`
	Hard struct {
		// Storage is -1 for unlimited.
		Storage int64 `json:"storage"`
	} `json:"hard"`
	Used struct {
		Storage int64 `json:"storage"`
	} `json:"used"`
}

func (c *Client) Quotas(ctx context.Context) ([]Quota, error) {
	items, _, err := list[Quota](ctx, c, "quotas", url.Values{"reference": {"project"}}, 0)
	return items, err
}

// Repository is one repository. Name includes the project: "project/name".
type Repository struct {
	ID            int64  `json:"id"`
	ProjectID     int64  `json:"project_id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ArtifactCount int64  `json:"artifact_count"`
	PullCount     int64  `json:"pull_count"`
	Created       Time   `json:"creation_time"`
	Updated       Time   `json:"update_time"`
}

// Split returns the project and the repository name inside it.
func (r Repository) Split() (project, name string) {
	project, name, _ = strings.Cut(r.Name, "/")
	return project, name
}

// RepositoryFilter narrows a repository listing.
type RepositoryFilter struct {
	// Project lists one project; empty lists every repository in sight.
	Project string
	// NameContains keeps repositories whose name contains this text.
	NameContains string
	// Sort is a field name, with a leading - for descending.
	Sort string
}

func (c *Client) Repositories(ctx context.Context, f RepositoryFilter, limit int) ([]Repository, bool, error) {
	params := url.Values{}
	if f.NameContains != "" {
		params.Set("q", "name=~"+f.NameContains)
	}
	if f.Sort != "" {
		params.Set("sort", f.Sort)
	}
	path := "repositories"
	if f.Project != "" {
		path = "projects/" + url.PathEscape(f.Project) + "/repositories"
	}
	return list[Repository](ctx, c, path, params, limit)
}

// ScanOverview is the result of the last vulnerability scan of an artifact.
type ScanOverview struct {
	Status   string `json:"scan_status"`
	Severity string `json:"severity"`
	EndTime  Time   `json:"end_time"`
	Scanner  struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"scanner"`
	Summary struct {
		Total   int64            `json:"total"`
		Fixable int64            `json:"fixable"`
		Summary map[string]int64 `json:"summary"`
	} `json:"summary"`
}

// Tag is a tag of an artifact.
type Tag struct {
	Name string `json:"name"`
}

// Artifact is one image or other OCI artifact.
type Artifact struct {
	ID             int64                   `json:"id"`
	Digest         string                  `json:"digest"`
	Type           string                  `json:"type"`
	Size           int64                   `json:"size"`
	RepositoryName string                  `json:"repository_name"`
	PushTime       Time                    `json:"push_time"`
	PullTime       Time                    `json:"pull_time"`
	Tags           []Tag                   `json:"tags"`
	ScanOverview   map[string]ScanOverview `json:"scan_overview"`
	ExtraAttrs     struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	} `json:"extra_attrs"`
}

// Scan returns the vulnerability scan among the artifact's reports, or nil.
func (a Artifact) Scan() *ScanOverview {
	for mime, overview := range a.ScanOverview {
		if strings.Contains(mime, "vulnerability") {
			return &overview
		}
	}
	return nil
}

// Artifacts lists the artifacts of one repository, newest push first.
// repository is the name inside the project and may contain slashes.
func (c *Client) Artifacts(ctx context.Context, project, repository string, limit int) ([]Artifact, bool, error) {
	// Harbor wants the slashes of a nested repository name encoded twice.
	repo := url.PathEscape(url.PathEscape(repository))
	path := "projects/" + url.PathEscape(project) + "/repositories/" + repo + "/artifacts"
	params := url.Values{
		"with_tag":           {"true"},
		"with_scan_overview": {"true"},
		"with_label":         {"false"},
		"sort":               {"-push_time"},
	}
	return list[Artifact](ctx, c, path, params, limit)
}

// SecuritySummary is the vulnerability total of everything that was scanned.
type SecuritySummary struct {
	Critical       int64 `json:"critical_cnt"`
	High           int64 `json:"high_cnt"`
	Medium         int64 `json:"medium_cnt"`
	Low            int64 `json:"low_cnt"`
	None           int64 `json:"none_cnt"`
	Unknown        int64 `json:"unknown_cnt"`
	Total          int64 `json:"total_vuls"`
	Fixable        int64 `json:"fixable_cnt"`
	Scanned        int64 `json:"scanned_cnt"`
	TotalArtifacts int64 `json:"total_artifact"`
}

func (c *Client) SecuritySummary(ctx context.Context) (*SecuritySummary, error) {
	var s SecuritySummary
	if _, err := c.get(ctx, "security/summary", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Vulnerability is one finding in one artifact.
type Vulnerability struct {
	CVE            string   `json:"cve_id"`
	Severity       string   `json:"severity"`
	CVSS3          float64  `json:"cvss_v3_score"`
	Package        string   `json:"package"`
	Version        string   `json:"version"`
	FixedVersion   string   `json:"fixed_version"`
	Status         string   `json:"status"`
	ProjectID      int64    `json:"project_id"`
	RepositoryName string   `json:"repository_name"`
	Digest         string   `json:"digest"`
	Tags           []string `json:"tags"`
	Description    string   `json:"desc"`
	Links          []string `json:"links"`
}

// VulnerabilityFilter narrows the vulnerability list. Every field is an
// exact match, which is all Harbor offers.
type VulnerabilityFilter struct {
	Severity   string
	CVE        string
	Package    string
	Repository string // "project/name"
	ProjectID  int64
}

func (c *Client) Vulnerabilities(ctx context.Context, f VulnerabilityFilter, limit int) ([]Vulnerability, bool, error) {
	var conditions []string
	add := func(key, value string) {
		if value != "" {
			conditions = append(conditions, key+"="+value)
		}
	}
	add("severity", f.Severity)
	add("cve_id", f.CVE)
	add("package", f.Package)
	add("repository_name", f.Repository)
	if f.ProjectID > 0 {
		add("project_id", strconv.FormatInt(f.ProjectID, 10))
	}
	params := url.Values{"with_tag": {"true"}, "tune_count": {"true"}}
	if len(conditions) > 0 {
		params.Set("q", strings.Join(conditions, ","))
	}
	return list[Vulnerability](ctx, c, "security/vul", params, limit)
}

// ReplicationPolicy is a replication rule.
type ReplicationPolicy struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (c *Client) ReplicationPolicies(ctx context.Context) ([]ReplicationPolicy, error) {
	items, _, err := list[ReplicationPolicy](ctx, c, "replication/policies", nil, 0)
	return items, err
}

// ReplicationExecution is one run of a replication rule.
type ReplicationExecution struct {
	ID         int64  `json:"id"`
	PolicyID   int64  `json:"policy_id"`
	Status     string `json:"status"`
	StatusText string `json:"status_text"`
	Trigger    string `json:"trigger"`
	Start      Time   `json:"start_time"`
	End        Time   `json:"end_time"`
	Total      int64  `json:"total"`
	Succeed    int64  `json:"succeed"`
	Failed     int64  `json:"failed"`
	InProgress int64  `json:"in_progress"`
	Stopped    int64  `json:"stopped"`
}

// ReplicationExecutions lists runs, newest first. policyID 0 and status ""
// do not filter.
func (c *Client) ReplicationExecutions(ctx context.Context, policyID int64, status string, limit int) ([]ReplicationExecution, bool, error) {
	params := url.Values{}
	if policyID > 0 {
		params.Set("policy_id", strconv.FormatInt(policyID, 10))
	}
	if status != "" {
		params.Set("status", status)
	}
	return list[ReplicationExecution](ctx, c, "replication/executions", params, limit)
}

// GCRun is one run of garbage collection.
type GCRun struct {
	ID      int64  `json:"id"`
	Kind    string `json:"job_kind"`
	Status  string `json:"job_status"`
	Created Time   `json:"creation_time"`
	Updated Time   `json:"update_time"`
	// Parameters is a JSON document in a string.
	Parameters string `json:"job_parameters"`
}

// GCResult is what a garbage collection run was asked to do and what it freed.
type GCResult struct {
	DryRun          bool  `json:"dry_run"`
	DeleteUntagged  bool  `json:"delete_untagged"`
	FreedSpace      int64 `json:"freed_space"`
	PurgedBlobs     int64 `json:"purged_blobs"`
	PurgedManifests int64 `json:"purged_manifests"`
}

// Result parses the run's parameters; a run without them gives zeros.
func (g GCRun) Result() GCResult {
	var r GCResult
	_ = json.Unmarshal([]byte(g.Parameters), &r)
	return r
}

func (c *Client) GCHistory(ctx context.Context, limit int) ([]GCRun, bool, error) {
	return list[GCRun](ctx, c, "system/gc", url.Values{"sort": {"-creation_time"}}, limit)
}

// AuditLog is one entry of the audit log.
type AuditLog struct {
	ID           int64  `json:"id"`
	Time         Time   `json:"op_time"`
	Username     string `json:"username"`
	Operation    string `json:"operation"`
	ResourceType string `json:"resource_type"`
	Resource     string `json:"resource"`
	// Description and Succeeded come with Harbor 2.13 and later.
	Description string `json:"operation_description"`
	Succeeded   *bool  `json:"operation_result"`
}

// AuditFilter narrows the audit log. Text fields are exact matches.
type AuditFilter struct {
	From, To     time.Time
	Username     string
	Operation    string
	ResourceType string
}

const auditTime = "2006-01-02 15:04:05"

// AuditLogs lists entries between From and To, newest first. Harbor 2.13
// moved the log to a new endpoint; older versions are asked at the old one.
func (c *Client) AuditLogs(ctx context.Context, f AuditFilter, limit int) ([]AuditLog, bool, error) {
	var conditions []string
	if !f.From.IsZero() && !f.To.IsZero() {
		conditions = append(conditions, "op_time=["+f.From.UTC().Format(auditTime)+"~"+f.To.UTC().Format(auditTime)+"]")
	}
	for _, kv := range [][2]string{{"username", f.Username}, {"operation", f.Operation}, {"resource_type", f.ResourceType}} {
		if kv[1] != "" {
			conditions = append(conditions, kv[0]+"="+kv[1])
		}
	}
	params := func() url.Values {
		p := url.Values{"sort": {"-op_time"}}
		if len(conditions) > 0 {
			p.Set("q", strings.Join(conditions, ","))
		}
		return p
	}
	items, more, err := list[AuditLog](ctx, c, "auditlog-exts", params(), limit)
	if IsNotFound(err) {
		return list[AuditLog](ctx, c, "audit-logs", params(), limit)
	}
	return items, more, err
}

// JobQueue is the queue of one kind of background job.
type JobQueue struct {
	Type string `json:"job_type"`
	// Count is the number of jobs waiting.
	Count int64 `json:"count"`
	// Latency is how long the oldest waiting job has waited, in seconds.
	Latency int64 `json:"latency"`
	Paused  bool  `json:"paused"`
}

func (c *Client) JobQueues(ctx context.Context) ([]JobQueue, error) {
	var q []JobQueue
	if _, err := c.get(ctx, "jobservice/queues", nil, &q); err != nil {
		return nil, err
	}
	return q, nil
}

// Robot is a robot account.
type Robot struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Level       string `json:"level"`
	Disabled    bool   `json:"disable"`
	// ExpiresAt is a Unix time in seconds, or -1 for never.
	ExpiresAt int64 `json:"expires_at"`
	Created   Time  `json:"creation_time"`
}

// Expires returns nil for an account that never expires.
func (r Robot) Expires() *time.Time {
	if r.ExpiresAt <= 0 {
		return nil
	}
	t := time.Unix(r.ExpiresAt, 0).UTC()
	return &t
}

// Robots lists the system-level robot accounts.
func (c *Client) Robots(ctx context.Context) ([]Robot, error) {
	items, _, err := list[Robot](ctx, c, "robots", nil, 0)
	return items, err
}
