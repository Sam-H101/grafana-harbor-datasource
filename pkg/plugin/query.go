package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sam-H101/grafana-harbor-datasource/pkg/harbor"
	"github.com/Sam-H101/grafana-harbor-datasource/pkg/models"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

const (
	// maxConcurrentQueries bounds how many queries of one request run at once.
	maxConcurrentQueries = 5
	// maxParallelRequests bounds the requests one query makes at once.
	maxParallelRequests = 4
)

var (
	errNoSuchProject = errors.New("no such project")
	errNoSuchPolicy  = errors.New("no such replication rule")
)

// QueryData runs the queries of one request concurrently (bounded) and
// returns one response per RefID.
func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	resp := backend.NewQueryDataResponse()
	if d.settingsErr != nil {
		p := d.explain(d.settingsErr)
		for _, q := range req.Queries {
			resp.Responses[q.RefID] = p.response()
		}
		return resp, nil
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, maxConcurrentQueries)
	)
	for _, q := range req.Queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := d.query(ctx, q)
			mu.Lock()
			resp.Responses[q.RefID] = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	return resp, nil
}

func (d *Datasource) query(ctx context.Context, q backend.DataQuery) backend.DataResponse {
	qm, err := models.ParseQuery(q.JSON)
	if err != nil {
		return d.explain(err).response()
	}
	ctx, cancel := context.WithTimeout(ctx, d.settings.Timeout)
	defer cancel()

	var frame *data.Frame
	switch qm.QueryType {
	case models.QueryProjects:
		frame, err = d.projects(ctx, qm)
		err = needs(err, "projects", permProjects)
	case models.QueryRepositories:
		frame, err = d.repositories(ctx, qm)
		err = needs(err, "repositories", permRepositories)
	case models.QueryArtifacts:
		frame, err = d.artifacts(ctx, qm)
		err = needs(err, "artifacts", permArtifacts)
	case models.QueryVulnerabilities:
		frame, err = d.vulnerabilities(ctx, qm)
		err = needs(err, "vulnerabilities", permSecurityHub)
	case models.QuerySecuritySummary:
		frame, err = d.securitySummary(ctx, qm)
		err = needs(err, "vulnerabilities", permSecurityHub)
	case models.QueryStatistics:
		frame, err = d.statistics(ctx, qm)
		err = needs(err, "projects", permProjects)
	case models.QueryHealth:
		frame, err = d.health(ctx, qm)
	case models.QueryReplications:
		frame, err = d.replications(ctx, qm)
		err = needs(err, "replications", permReplication)
	case models.QueryGarbageCollections:
		frame, err = d.garbageCollections(ctx, qm)
		err = needs(err, "garbage collection runs", permGC)
	case models.QueryAuditLogs:
		frame, err = d.auditLogs(ctx, qm, q.TimeRange)
		err = needs(err, "the audit log", permAuditLog)
	case models.QueryJobQueues:
		frame, err = d.jobQueues(ctx, qm)
		err = needs(err, "job queues", permJobQueues)
	case models.QueryRobots:
		frame, err = d.robots(ctx)
		err = needs(err, "robot accounts", permRobots)
	}
	if err != nil {
		p := d.explain(err)
		backend.Logger.Warn("Harbor query failed", "refId", q.RefID, "queryType", string(qm.QueryType), "status", int(p.status), "error", logSafe(p.message))
		return p.response()
	}
	frame.RefID = q.RefID
	return backend.DataResponse{Frames: data.Frames{frame}}
}

// forEach runs fn for every key, a few at a time, and returns the results in
// the order of keys. The first error cancels the rest.
func forEach[K, T any](ctx context.Context, keys []K, fn func(ctx context.Context, key K) (T, error)) ([]T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		out      = make([]T, len(keys))
		wg       sync.WaitGroup
		sem      = make(chan struct{}, maxParallelRequests)
		once     sync.Once
		firstErr error
	)
	for i, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			v, err := fn(ctx, key)
			if err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			out[i] = v
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// selectProjects returns the projects a query names, or all of them. A name
// Harbor does not list is an error that says so: Harbor itself answers
// "forbidden" for a project that does not exist, which sends people looking
// for a permission problem.
func (d *Datasource) selectProjects(ctx context.Context, names []string) ([]harbor.Project, error) {
	all, err := d.projectList(ctx)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return all, nil
	}
	byName := make(map[string]harbor.Project, len(all))
	for _, p := range all {
		byName[p.Name] = p
	}
	out := make([]harbor.Project, 0, len(names))
	for _, name := range names {
		p, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w %q, or this login cannot see it", errNoSuchProject, name)
		}
		out = append(out, p)
	}
	return out, nil
}

func (d *Datasource) projects(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	projects, err := d.selectProjects(ctx, qm.Projects)
	if err != nil {
		return nil, err
	}
	sort.Slice(projects, func(a, b int) bool { return projects[a].Name < projects[b].Name })

	// Quotas need a permission of their own; without it the storage columns
	// stay blank.
	quotas, qerr := d.client.Quotas(ctx)
	if qerr != nil && !errors.Is(qerr, harbor.ErrForbidden) && !errors.Is(qerr, harbor.ErrUnauthorized) {
		return nil, qerr
	}
	byProject := make(map[int64]harbor.Quota, len(quotas))
	for _, q := range quotas {
		byProject[q.Ref.ID] = q
	}

	n := len(projects)
	var (
		name, owner = make([]string, n), make([]string, n)
		public      = make([]bool, n)
		repos       = make([]int64, n)
		used, hard  = make([]*int64, n), make([]*int64, n)
		percent     = make([]*float64, n)
		created     = make([]*time.Time, n)
	)
	for i, p := range projects {
		name[i], owner[i], public[i], repos[i], created[i] = p.Name, p.OwnerName, p.Public(), p.RepoCount, p.Created.Ptr()
		if q, ok := byProject[p.ID]; ok {
			u := q.Used.Storage
			used[i] = &u
			if q.Hard.Storage > 0 {
				h := q.Hard.Storage
				hard[i] = &h
				pct := float64(u) / float64(h) * 100
				percent[i] = &pct
			}
		}
	}

	if qm.Value != "" {
		var (
			items  []string
			values []float64
			unitOf = map[string]string{"storage": "bytes", "quotaPercent": "percent", "repositories": ""}[qm.Value]
		)
		for i := range projects {
			switch qm.Value {
			case "storage":
				if used[i] != nil {
					items, values = append(items, name[i]), append(values, float64(*used[i]))
				}
			case "quotaPercent":
				if percent[i] != nil {
					items, values = append(items, name[i]), append(values, *percent[i])
				}
			case "repositories":
				items, values = append(items, name[i]), append(values, float64(repos[i]))
			}
		}
		frame := d.numbers(qm.Value, "project", items, values, unitOf)
		switch {
		case qerr != nil && qm.Value != "repositories":
			return nil, needs(qerr, "project quotas", permQuotas)
		case len(items) == 0 && qm.Value == "quotaPercent":
			addNotice(frame, "No project has a storage quota.")
		case len(items) == 0:
			addNotice(frame, "No project matches.")
		}
		return frame, nil
	}

	frame := data.NewFrame("projects",
		named(data.NewField("project", nil, name), "Project"),
		boolField("public", "Access", public, "Public", "Private"),
		named(data.NewField("owner", nil, owner), "Owner"),
		named(data.NewField("repositories", nil, repos), "Repositories"),
		unit(named(data.NewField("storage_used", nil, used), "Storage used"), "bytes"),
		unit(named(data.NewField("storage_quota", nil, hard), "Quota"), "bytes"),
		unit(named(data.NewField("quota_used", nil, percent), "Quota used"), "percent"),
		named(data.NewField("created", nil, created), "Created"),
	)
	switch {
	case n == 0 && !d.client.HasCredentials():
		addNotice(frame, "Without a login only public projects are listed, and there is none. Enter a robot account in the data source settings.")
	case n == 0:
		addNotice(frame, "This login cannot see any project.")
	case qerr != nil:
		addNotice(frame, "Storage used and quota are blank because the account may not read quotas. Give the robot account the permission "+permQuotas+".")
	}
	return frame, nil
}

// repositorySort maps the sort option to Harbor's field name.
var repositorySort = map[string]string{
	"name": "name", "pulls": "pull_count", "artifacts": "artifact_count", "updated": "update_time",
}

func (d *Datasource) repositories(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	filter := harbor.RepositoryFilter{NameContains: qm.Repository}
	if field := repositorySort[qm.Sort]; field != "" {
		if qm.Descending {
			field = "-" + field
		}
		filter.Sort = field
	}

	var (
		repos []harbor.Repository
		more  bool
	)
	if len(qm.Projects) == 0 {
		var err error
		if repos, more, err = d.client.Repositories(ctx, filter, qm.Limit); err != nil {
			return nil, err
		}
	} else {
		projects, err := d.selectProjects(ctx, qm.Projects)
		if err != nil {
			return nil, err
		}
		type page struct {
			items []harbor.Repository
			more  bool
		}
		pages, err := forEach(ctx, projects, func(ctx context.Context, p harbor.Project) (page, error) {
			f := filter
			f.Project = p.Name
			items, more, err := d.client.Repositories(ctx, f, qm.Limit)
			return page{items, more}, err
		})
		if err != nil {
			return nil, err
		}
		for _, p := range pages {
			repos = append(repos, p.items...)
			more = more || p.more
		}
		sortRepositories(repos, qm.Sort, qm.Descending)
		if len(repos) > qm.Limit {
			repos, more = repos[:qm.Limit], true
		}
	}

	n := len(repos)
	var (
		project, name, full, description = make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		artifacts, pulls                 = make([]int64, n), make([]int64, n)
		updated, created                 = make([]*time.Time, n), make([]*time.Time, n)
	)
	for i, r := range repos {
		project[i], name[i] = r.Split()
		full[i], description[i] = r.Name, r.Description
		artifacts[i], pulls[i] = r.ArtifactCount, r.PullCount
		updated[i], created[i] = r.Updated.Ptr(), r.Created.Ptr()
	}
	frame := data.NewFrame("repositories",
		named(data.NewField("project", nil, project), "Project"),
		named(data.NewField("repository", nil, name), "Repository"),
		named(data.NewField("artifacts", nil, artifacts), "Artifacts"),
		named(data.NewField("pulls", nil, pulls), "Pulls"),
		named(data.NewField("updated", nil, updated), "Updated"),
		named(data.NewField("created", nil, created), "Created"),
		named(data.NewField("description", nil, description), "Description"),
		named(data.NewField("full_name", nil, full), "Full name"),
	)
	limitNotice(frame, "repositories", n, more)
	return frame, nil
}

func sortRepositories(repos []harbor.Repository, by string, descending bool) {
	if by == "" {
		by, descending = "name", false
	}
	less := func(a, b harbor.Repository) bool {
		switch by {
		case "pulls":
			return a.PullCount < b.PullCount
		case "artifacts":
			return a.ArtifactCount < b.ArtifactCount
		case "updated":
			return a.Updated.Before(b.Updated.Time)
		}
		return a.Name < b.Name
	}
	sort.SliceStable(repos, func(i, j int) bool {
		if descending {
			return less(repos[j], repos[i])
		}
		return less(repos[i], repos[j])
	})
}

func (d *Datasource) artifacts(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	projects, err := d.selectProjects(ctx, qm.Projects)
	if err != nil {
		return nil, err
	}

	// Which repositories to list the artifacts of.
	type target struct{ project, repository string }
	var (
		targets []target
		skipped bool
	)
	if qm.Repository != "" {
		targets = []target{{projects[0].Name, qm.Repository}}
	} else {
		type listing struct {
			repos []harbor.Repository
			more  bool
		}
		lists, err := forEach(ctx, projects, func(ctx context.Context, p harbor.Project) (listing, error) {
			repos, more, err := d.client.Repositories(ctx, harbor.RepositoryFilter{Project: p.Name, Sort: "-update_time"}, models.MaxRepositoriesPerQuery)
			return listing{repos, more}, needs(err, "repositories", permRepositories)
		})
		if err != nil {
			return nil, err
		}
		for _, l := range lists {
			skipped = skipped || l.more
			for _, r := range l.repos {
				project, name := r.Split()
				targets = append(targets, target{project, name})
			}
		}
		if len(targets) > models.MaxRepositoriesPerQuery {
			targets, skipped = targets[:models.MaxRepositoriesPerQuery], true
		}
	}

	type page struct {
		target target
		items  []harbor.Artifact
		more   bool
	}
	pages, err := forEach(ctx, targets, func(ctx context.Context, t target) (page, error) {
		items, more, err := d.client.Artifacts(ctx, t.project, t.repository, qm.Limit)
		if err == nil && len(items) == 0 && qm.Repository != "" {
			// Harbor answers with an empty list for a repository that does
			// not exist: say so instead of showing an empty table.
			if exists, xerr := d.repositoryExists(ctx, t.project, t.repository); xerr == nil && !exists {
				return page{}, fmt.Errorf("%w: there is no repository %s/%s", models.ErrInvalidQuery, t.project, t.repository)
			}
		}
		return page{t, items, more}, err
	})
	if err != nil {
		return nil, err
	}

	type row struct {
		target target
		harbor.Artifact
	}
	var (
		rows []row
		more bool
	)
	for _, p := range pages {
		more = more || p.more
		for _, a := range p.items {
			rows = append(rows, row{p.target, a})
		}
	}
	// Newest push first, across repositories.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].PushTime.After(rows[j].PushTime.Time) })
	if len(rows) > qm.Limit {
		rows, more = rows[:qm.Limit], true
	}

	n := len(rows)
	var (
		project, repository, tags, digest, kind, platform = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		size                                              = make([]int64, n)
		pushed, pulled, scanned                           = make([]*time.Time, n), make([]*time.Time, n), make([]*time.Time, n)
		scanStatus, severity                              = make([]string, n), make([]string, n)
		total, critical, high, medium, low, fixable       = make([]*int64, n), make([]*int64, n), make([]*int64, n), make([]*int64, n), make([]*int64, n), make([]*int64, n)
	)
	for i, r := range rows {
		project[i], repository[i], digest[i], kind[i], size[i] = r.target.project, r.target.repository, r.Digest, r.Type, r.Size
		names := make([]string, len(r.Tags))
		for j, t := range r.Tags {
			names[j] = t.Name
		}
		sort.Strings(names)
		tags[i] = strings.Join(names, ", ")
		if r.ExtraAttrs.OS != "" || r.ExtraAttrs.Architecture != "" {
			platform[i] = strings.Trim(r.ExtraAttrs.OS+"/"+r.ExtraAttrs.Architecture, "/")
		}
		pushed[i], pulled[i] = r.PushTime.Ptr(), r.PullTime.Ptr()
		scanStatus[i] = "Not scanned"
		if scan := r.Scan(); scan != nil {
			scanStatus[i], severity[i], scanned[i] = scan.Status, scan.Severity, scan.EndTime.Ptr()
			if scan.Status == "Success" {
				count := func(level string) *int64 { v := scan.Summary.Summary[level]; return &v }
				t, f := scan.Summary.Total, scan.Summary.Fixable
				total[i], fixable[i] = &t, &f
				critical[i], high[i], medium[i], low[i] = count("Critical"), count("High"), count("Medium"), count("Low")
			}
		}
	}
	frame := data.NewFrame("artifacts",
		named(data.NewField("pushed", nil, pushed), "Pushed"),
		named(data.NewField("project", nil, project), "Project"),
		named(data.NewField("repository", nil, repository), "Repository"),
		named(data.NewField("tags", nil, tags), "Tags"),
		unit(named(data.NewField("size", nil, size), "Size"), "bytes"),
		named(data.NewField("pulled", nil, pulled), "Last pull"),
		named(data.NewField("scan_status", nil, scanStatus), "Scan"),
		severityField(severity, "Highest severity"),
		named(data.NewField("vulnerabilities", nil, total), "Vulnerabilities"),
		named(data.NewField("critical", nil, critical), "Critical"),
		named(data.NewField("high", nil, high), "High"),
		named(data.NewField("medium", nil, medium), "Medium"),
		named(data.NewField("low", nil, low), "Low"),
		named(data.NewField("fixable", nil, fixable), "Fixable"),
		named(data.NewField("scanned", nil, scanned), "Scanned"),
		named(data.NewField("type", nil, kind), "Type"),
		named(data.NewField("platform", nil, platform), "Platform"),
		named(data.NewField("digest", nil, digest), "Digest"),
	)
	limitNotice(frame, "artifacts", n, more)
	if skipped {
		addNotice(frame, fmt.Sprintf("Only the %d most recently updated repositories were searched. Choose fewer projects or name a repository.", models.MaxRepositoriesPerQuery))
	}
	return frame, nil
}

func (d *Datasource) repositoryExists(ctx context.Context, project, repository string) (bool, error) {
	repos, _, err := d.client.Repositories(ctx, harbor.RepositoryFilter{Project: project, NameContains: repository}, 0)
	if err != nil {
		return false, err
	}
	for _, r := range repos {
		if r.Name == project+"/"+repository {
			return true, nil
		}
	}
	return false, nil
}

func (d *Datasource) vulnerabilities(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	all, err := d.projectList(ctx)
	if err != nil {
		return nil, needs(err, "projects", permProjects)
	}
	projectName := make(map[int64]string, len(all))
	for _, p := range all {
		projectName[p.ID] = p.Name
	}

	base := harbor.VulnerabilityFilter{Severity: qm.Severity, CVE: qm.CVE, Package: qm.Package, Repository: qm.Repository}
	filters := []harbor.VulnerabilityFilter{base}
	if len(qm.Projects) > 0 {
		selected, err := d.selectProjects(ctx, qm.Projects)
		if err != nil {
			return nil, err
		}
		filters = filters[:0]
		for _, p := range selected {
			f := base
			f.ProjectID = p.ID
			filters = append(filters, f)
		}
	}
	type page struct {
		items []harbor.Vulnerability
		more  bool
	}
	pages, err := forEach(ctx, filters, func(ctx context.Context, f harbor.VulnerabilityFilter) (page, error) {
		items, more, err := d.client.Vulnerabilities(ctx, f, qm.Limit)
		return page{items, more}, err
	})
	if err != nil {
		return nil, err
	}
	var (
		vulns []harbor.Vulnerability
		more  bool
	)
	for _, p := range pages {
		vulns = append(vulns, p.items...)
		more = more || p.more
	}
	if len(vulns) > qm.Limit {
		vulns, more = vulns[:qm.Limit], true
	}

	n := len(vulns)
	var (
		cve, severity, pkg, version, fixed, status = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		project, repository, tags, digest          = make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		description, link                          = make([]string, n), make([]string, n)
		score                                      = make([]*float64, n)
	)
	for i, v := range vulns {
		cve[i], severity[i], pkg[i], version[i], fixed[i], status[i] = v.CVE, v.Severity, v.Package, v.Version, v.FixedVersion, v.Status
		project[i] = projectName[v.ProjectID]
		repository[i] = strings.TrimPrefix(v.RepositoryName, project[i]+"/")
		tags[i], digest[i] = strings.Join(v.Tags, ", "), v.Digest
		description[i] = strings.Join(strings.Fields(v.Description), " ")
		if len(v.Links) > 0 {
			link[i] = v.Links[0]
		}
		if v.CVSS3 > 0 {
			s := v.CVSS3
			score[i] = &s
		}
	}
	linkField := named(data.NewField("link", nil, link), "Link")
	frame := data.NewFrame("vulnerabilities",
		named(data.NewField("cve", nil, cve), "CVE"),
		severityField(severity, "Severity"),
		named(data.NewField("cvss", nil, score), "CVSS v3"),
		named(data.NewField("package", nil, pkg), "Package"),
		named(data.NewField("version", nil, version), "Version"),
		named(data.NewField("fixed_version", nil, fixed), "Fixed in"),
		named(data.NewField("status", nil, status), "Status"),
		named(data.NewField("project", nil, project), "Project"),
		named(data.NewField("repository", nil, repository), "Repository"),
		named(data.NewField("tags", nil, tags), "Tags"),
		named(data.NewField("description", nil, description), "Description"),
		linkField,
		named(data.NewField("digest", nil, digest), "Digest"),
	)
	limitNotice(frame, "vulnerabilities", n, more)
	return frame, nil
}

func (d *Datasource) securitySummary(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	s, err := d.client.SecuritySummary(ctx)
	if err != nil {
		return nil, err
	}
	return d.measures("vulnerabilities", qm.Measure, []measure{
		{"critical", "Critical", float64(s.Critical), ""},
		{"high", "High", float64(s.High), ""},
		{"medium", "Medium", float64(s.Medium), ""},
		{"low", "Low", float64(s.Low), ""},
		{"unknown", "Unknown", float64(s.Unknown + s.None), ""},
		{"total", "Total", float64(s.Total), ""},
		{"fixable", "Fixable", float64(s.Fixable), ""},
		{"scanned_artifacts", "Scanned artifacts", float64(s.Scanned), ""},
		{"artifacts", "Artifacts", float64(s.TotalArtifacts), ""},
	}), nil
}

// statistics adds up what the account can see. Harbor's own statistics give
// the overall totals to system administrators only, and zeros to a robot
// account, so they are not used.
func (d *Datasource) statistics(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	projects, err := d.projectList(ctx)
	if err != nil {
		return nil, err
	}
	var public, repos float64
	for _, p := range projects {
		if p.Public() {
			public++
		}
		repos += float64(p.RepoCount)
	}
	out := []measure{
		{"projects", "Projects", float64(len(projects)), ""},
		{"public_projects", "Public projects", public, ""},
		{"private_projects", "Private projects", float64(len(projects)) - public, ""},
		{"repositories", "Repositories", repos, ""},
	}

	var missing []string
	allowed := func(err error, what, permission string, numbers ...string) (bool, error) {
		refused := errors.Is(err, harbor.ErrForbidden) || errors.Is(err, harbor.ErrUnauthorized)
		switch {
		case err == nil:
			return true, nil
		case refused && contains(numbers, qm.Measure):
			// The one number asked for is one the account may not read.
			return false, &denied{what: what, permission: permission}
		case refused:
			missing = append(missing, what+" ("+permission+")")
			return false, nil
		}
		return false, err
	}

	quotas, err := d.client.Quotas(ctx)
	if ok, err := allowed(err, "storage used", permQuotas, "storage_used"); err != nil {
		return nil, err
	} else if ok {
		var used float64
		for _, q := range quotas {
			used += float64(q.Used.Storage)
		}
		out = append(out, measure{"storage_used", "Storage used", used, "bytes"})
	}

	volumes, err := d.client.Volumes(ctx)
	if ok, err := allowed(err, "the registry's disk", permVolumes, "disk_total", "disk_free", "disk_free_percent"); err != nil {
		return nil, err
	} else if ok && len(volumes) > 0 {
		var total, free float64
		for _, v := range volumes {
			total, free = total+float64(v.Total), free+float64(v.Free)
		}
		out = append(out, measure{"disk_total", "Disk size", total, "bytes"}, measure{"disk_free", "Disk free", free, "bytes"})
		if total > 0 {
			out = append(out, measure{"disk_free_percent", "Disk free %", free / total * 100, "percent"})
		}
	}

	frame := d.measures("statistics", qm.Measure, out)
	if len(missing) > 0 && qm.Measure == "" {
		addNotice(frame, "Left out because the account may not read them: "+strings.Join(missing, "; ")+".")
	}
	return frame, nil
}

func (d *Datasource) health(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	h, err := d.client.Health(ctx)
	if err != nil {
		return nil, err
	}
	var (
		name, status, message []string
		items                 []string
		up                    []float64
	)
	for _, c := range h.Components {
		healthy := strings.EqualFold(c.Status, "healthy")
		items = append(items, c.Name)
		if healthy {
			up = append(up, 1)
		} else {
			up = append(up, 0)
		}
		if qm.UnhealthyOnly && healthy {
			continue
		}
		name, status, message = append(name, c.Name), append(status, c.Status), append(message, strings.Join(strings.Fields(c.Error), " "))
	}
	if qm.Value == "status" {
		return d.numbers("healthy", "component", items, up, ""), nil
	}
	statusField := named(data.NewField("status", nil, orEmpty(status)), "Status")
	statusField.Config.Mappings = data.ValueMappings{data.ValueMapper{
		"healthy":   {Text: "Healthy", Color: "green"},
		"unhealthy": {Text: "Unhealthy", Color: "red"},
	}}
	frame := data.NewFrame("health",
		named(data.NewField("component", nil, orEmpty(name)), "Component"),
		statusField,
		named(data.NewField("error", nil, orEmpty(message)), "Error"),
	)
	if qm.UnhealthyOnly && len(name) == 0 {
		addNotice(frame, "Every component is healthy.")
	}
	return frame, nil
}

func (d *Datasource) replications(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	policies, err := d.client.ReplicationPolicies(ctx)
	if err != nil {
		return nil, err
	}
	policyName := make(map[int64]string, len(policies))
	var policyID int64
	for _, p := range policies {
		policyName[p.ID] = p.Name
		if qm.Policy != "" && p.Name == qm.Policy {
			policyID = p.ID
		}
	}
	if qm.Policy != "" && policyID == 0 {
		return nil, fmt.Errorf("%w %q", errNoSuchPolicy, qm.Policy)
	}

	runs, more, err := d.client.ReplicationExecutions(ctx, policyID, qm.Status, qm.Limit)
	if err != nil {
		return nil, err
	}
	n := len(runs)
	var (
		id                                         = make([]int64, n)
		policy, status, trigger, message           = make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		started, ended                             = make([]*time.Time, n), make([]*time.Time, n)
		duration                                   = make([]*float64, n)
		total, succeeded, failed, running, stopped = make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
	)
	for i, r := range runs {
		id[i], status[i], trigger[i], message[i] = r.ID, r.Status, r.Trigger, r.StatusText
		policy[i] = policyName[r.PolicyID]
		if policy[i] == "" {
			policy[i] = fmt.Sprintf("rule %d (deleted)", r.PolicyID)
		}
		started[i], ended[i] = r.Start.Ptr(), r.End.Ptr()
		duration[i] = seconds(r.Start, r.End)
		total[i], succeeded[i], failed[i], running[i], stopped[i] = r.Total, r.Succeed, r.Failed, r.InProgress, r.Stopped
	}
	frame := data.NewFrame("replications",
		named(data.NewField("started", nil, started), "Started"),
		named(data.NewField("rule", nil, policy), "Rule"),
		jobStatusField(status),
		named(data.NewField("trigger", nil, trigger), "Trigger"),
		unit(named(data.NewField("duration", nil, duration), "Duration"), "s"),
		named(data.NewField("total", nil, total), "Tasks"),
		named(data.NewField("succeeded", nil, succeeded), "Succeeded"),
		named(data.NewField("failed", nil, failed), "Failed"),
		named(data.NewField("in_progress", nil, running), "In progress"),
		named(data.NewField("stopped", nil, stopped), "Stopped"),
		named(data.NewField("ended", nil, ended), "Ended"),
		named(data.NewField("message", nil, message), "Message"),
		named(data.NewField("id", nil, id), "ID"),
	)
	if n == 0 && len(policies) == 0 {
		addNotice(frame, "This Harbor has no replication rules.")
	} else {
		limitNotice(frame, "replication runs", n, more)
	}
	return frame, nil
}

func (d *Datasource) garbageCollections(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	runs, more, err := d.client.GCHistory(ctx, qm.Limit)
	if err != nil {
		return nil, err
	}
	n := len(runs)
	var (
		id                    = make([]int64, n)
		status, trigger       = make([]string, n), make([]string, n)
		started, ended        = make([]*time.Time, n), make([]*time.Time, n)
		duration              = make([]*float64, n)
		dryRun, untagged      = make([]bool, n), make([]bool, n)
		freed, blobs, manifes = make([]int64, n), make([]int64, n), make([]int64, n)
	)
	for i, r := range runs {
		res := r.Result()
		id[i], status[i], trigger[i] = r.ID, r.Status, strings.ToLower(r.Kind)
		started[i], ended[i] = r.Created.Ptr(), r.Updated.Ptr()
		if finished(r.Status) {
			duration[i] = seconds(r.Created, r.Updated)
		} else {
			ended[i] = nil
		}
		dryRun[i], untagged[i] = res.DryRun, res.DeleteUntagged
		freed[i], blobs[i], manifes[i] = res.FreedSpace, res.PurgedBlobs, res.PurgedManifests
	}
	frame := data.NewFrame("garbage_collections",
		named(data.NewField("started", nil, started), "Started"),
		jobStatusField(status),
		named(data.NewField("trigger", nil, trigger), "Trigger"),
		unit(named(data.NewField("duration", nil, duration), "Duration"), "s"),
		unit(named(data.NewField("freed_space", nil, freed), "Freed"), "bytes"),
		named(data.NewField("purged_blobs", nil, blobs), "Blobs removed"),
		named(data.NewField("purged_manifests", nil, manifes), "Manifests removed"),
		boolField("dry_run", "Dry run", dryRun, "Yes", "No"),
		boolField("delete_untagged", "Deletes untagged", untagged, "Yes", "No"),
		named(data.NewField("ended", nil, ended), "Ended"),
		named(data.NewField("id", nil, id), "ID"),
	)
	if n == 0 {
		addNotice(frame, "Garbage collection has not run yet.")
	} else {
		limitNotice(frame, "garbage collection runs", n, more)
	}
	return frame, nil
}

func (d *Datasource) auditLogs(ctx context.Context, qm *models.QueryModel, tr backend.TimeRange) (*data.Frame, error) {
	filter := harbor.AuditFilter{From: tr.From, To: tr.To, Username: qm.User, Operation: qm.Operation, ResourceType: qm.ResourceType}
	entries, more, err := d.client.AuditLogs(ctx, filter, qm.Limit)
	if err != nil {
		return nil, err
	}
	n := len(entries)
	var (
		at                                                     = make([]*time.Time, n)
		user, operation, resourceType, resource, text, outcome = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	)
	for i, e := range entries {
		at[i], user[i], operation[i], resourceType[i], resource[i] = e.Time.Ptr(), e.Username, e.Operation, e.ResourceType, e.Resource
		text[i] = e.Description
		if text[i] == "" {
			text[i] = strings.TrimSpace(e.Operation + " " + e.ResourceType + ": " + e.Resource)
		}
		if e.Succeeded != nil {
			outcome[i] = map[bool]string{true: "Succeeded", false: "Failed"}[*e.Succeeded]
		}
	}
	frame := data.NewFrame("audit_log",
		named(data.NewField("time", nil, at), "Time"),
		named(data.NewField("user", nil, user), "User"),
		named(data.NewField("operation", nil, operation), "Operation"),
		named(data.NewField("resource_type", nil, resourceType), "Resource type"),
		named(data.NewField("resource", nil, resource), "Resource"),
		named(data.NewField("description", nil, text), "Description"),
		named(data.NewField("result", nil, outcome), "Result"),
	)
	if n == 0 {
		addNotice(frame, "No audit log entries in this time range. An account that is neither a system administrator nor allowed to list the audit log sees none.")
	} else {
		limitNotice(frame, "audit log entries", n, more)
	}
	return frame, nil
}

func (d *Datasource) jobQueues(ctx context.Context, qm *models.QueryModel) (*data.Frame, error) {
	queues, err := d.client.JobQueues(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(queues, func(a, b int) bool { return queues[a].Type < queues[b].Type })
	n := len(queues)
	var (
		kind             = make([]string, n)
		pending, latency = make([]int64, n), make([]int64, n)
		paused           = make([]bool, n)
		values           = make([]float64, n)
	)
	for i, q := range queues {
		kind[i], pending[i], latency[i], paused[i] = q.Type, q.Count, q.Latency, q.Paused
		values[i] = float64(q.Count)
		if qm.Value == "latency" {
			values[i] = float64(q.Latency)
		}
	}
	if qm.Value != "" {
		return d.numbers(qm.Value, "job_type", kind, values, map[string]string{"latency": "s"}[qm.Value]), nil
	}
	return data.NewFrame("job_queues",
		named(data.NewField("job_type", nil, kind), "Job type"),
		named(data.NewField("pending", nil, pending), "Pending"),
		unit(named(data.NewField("latency", nil, latency), "Oldest waits"), "s"),
		boolField("paused", "Paused", paused, "Paused", "Running"),
	), nil
}

func (d *Datasource) robots(ctx context.Context) (*data.Frame, error) {
	robots, err := d.client.Robots(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(robots, func(a, b int) bool { return robots[a].Name < robots[b].Name })
	n := len(robots)
	var (
		name, level, description = make([]string, n), make([]string, n), make([]string, n)
		enabled                  = make([]bool, n)
		expires, created         = make([]*time.Time, n), make([]*time.Time, n)
		daysLeft                 = make([]*float64, n)
	)
	now := d.now()
	for i, r := range robots {
		name[i], level[i], description[i], enabled[i] = r.Name, r.Level, r.Description, !r.Disabled
		expires[i], created[i] = r.Expires(), r.Created.Ptr()
		if expires[i] != nil {
			days := expires[i].Sub(now).Hours() / 24
			daysLeft[i] = &days
		}
	}
	frame := data.NewFrame("robots",
		named(data.NewField("name", nil, name), "Robot account"),
		named(data.NewField("level", nil, level), "Level"),
		boolField("enabled", "State", enabled, "Enabled", "Disabled"),
		named(data.NewField("expires", nil, expires), "Expires"),
		unit(named(data.NewField("days_left", nil, daysLeft), "Days left"), "d"),
		named(data.NewField("description", nil, description), "Description"),
		named(data.NewField("created", nil, created), "Created"),
	)
	if n == 0 {
		addNotice(frame, "There are no system-level robot accounts.")
	}
	return frame, nil
}

// finished reports whether a job status is final.
func finished(status string) bool {
	switch strings.ToLower(status) {
	case "success", "succeed", "error", "failed", "stopped":
		return true
	}
	return false
}

// seconds returns how long something took, or nil when it has not ended.
func seconds(start, end harbor.Time) *float64 {
	if start.IsZero() || end.IsZero() || end.Before(start.Time) {
		return nil
	}
	s := end.Sub(start.Time).Seconds()
	return &s
}

// measure is one number of a frame built by measures.
type measure struct {
	name, display string
	value         float64
	unit          string
}

// measures builds a frame of unrelated numbers that stat panels and alert
// rules take: one row at the current time and one field per number, each
// with a label of its own. only keeps one of them ("" keeps all).
func (d *Datasource) measures(name, only string, items []measure) *data.Frame {
	frame := data.NewFrame(name, data.NewField("time", nil, []time.Time{d.now().UTC()}))
	for _, m := range items {
		if only != "" && m.name != only {
			continue
		}
		f := data.NewField(m.name, data.Labels{"measure": m.name}, []float64{m.value})
		f.Config = &data.FieldConfig{DisplayNameFromDS: m.display, Unit: m.unit}
		frame.Fields = append(frame.Fields, f)
	}
	frame.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesWide, TypeVersion: data.FrameTypeVersion{0, 1}}
	return frame
}

// numbers builds the frame that alert rules, stat panels and gauges take for
// one number per item: one row at the current time and one field per item.
// Each field carries a label, so that an alert rule gets one instance per item.
func (d *Datasource) numbers(name, label string, items []string, values []float64, unitName string) *data.Frame {
	frame := data.NewFrame(name, data.NewField("time", nil, []time.Time{d.now().UTC()}))
	for i, item := range items {
		f := data.NewField(name, data.Labels{label: item}, []float64{values[i]})
		f.Config = &data.FieldConfig{DisplayNameFromDS: item, Unit: unitName}
		frame.Fields = append(frame.Fields, f)
	}
	frame.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesWide, TypeVersion: data.FrameTypeVersion{0, 1}}
	return frame
}

func limitNotice(frame *data.Frame, what string, n int, more bool) {
	switch {
	case more:
		addNotice(frame, fmt.Sprintf("Showing the first %d %s. Narrow the query or raise the limit (up to %d) to see others.", n, what, models.MaxLimit))
	case n == 0:
		addNotice(frame, "No matching "+what+".")
	}
}

func boolField(name, display string, values []bool, yes, no string) *data.Field {
	if values == nil {
		values = []bool{}
	}
	f := named(data.NewField(name, nil, values), display)
	f.Config.Mappings = data.ValueMappings{data.ValueMapper{
		"true":  {Text: yes},
		"false": {Text: no},
	}}
	return f
}

// severityField colours severities the way Harbor's own pages do.
func severityField(values []string, display string) *data.Field {
	f := named(data.NewField("severity", nil, orEmpty(values)), display)
	f.Config.Mappings = data.ValueMappings{data.ValueMapper{
		"Critical": {Text: "Critical", Color: "dark-red"},
		"High":     {Text: "High", Color: "orange"},
		"Medium":   {Text: "Medium", Color: "yellow"},
		"Low":      {Text: "Low", Color: "blue"},
		"None":     {Text: "None", Color: "green"},
	}}
	return f
}

func jobStatusField(values []string) *data.Field {
	f := named(data.NewField("status", nil, orEmpty(values)), "Status")
	f.Config.Mappings = data.ValueMappings{data.ValueMapper{
		"Succeed":    {Text: "Succeeded", Color: "green"},
		"Success":    {Text: "Succeeded", Color: "green"},
		"Failed":     {Text: "Failed", Color: "red"},
		"Error":      {Text: "Failed", Color: "red"},
		"InProgress": {Text: "In progress", Color: "blue"},
		"Running":    {Text: "Running", Color: "blue"},
		"Pending":    {Text: "Pending", Color: "blue"},
		"Stopped":    {Text: "Stopped", Color: "orange"},
	}}
	return f
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func named(f *data.Field, display string) *data.Field {
	if f.Config == nil {
		f.Config = &data.FieldConfig{}
	}
	f.Config.DisplayName = display
	return f
}

func unit(f *data.Field, u string) *data.Field {
	if f.Config == nil {
		f.Config = &data.FieldConfig{}
	}
	f.Config.Unit = u
	return f
}

func addNotice(frame *data.Frame, text string) {
	if text != "" {
		frame.AppendNotices(data.Notice{Severity: data.NoticeSeverityInfo, Text: text})
	}
}
