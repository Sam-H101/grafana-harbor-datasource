package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Sam-H101/grafana-harbor-datasource/pkg/harbor"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

const healthTimeout = 20 * time.Second

// Permissions a robot account needs for each area, as Harbor's robot account
// form names them ("resource: action", under System or Project).
const (
	permProjects     = "Project: List (system)"
	permRepositories = "Repository: List (project)"
	permArtifacts    = "Artifact: List (project)"
	permQuotas       = "Quota: List (system)"
	permVolumes      = "System volumes: Read (system)"
	permSecurityHub  = "Security hub: List and Read (system)"
	permReplication  = "Replication: List and Replication policy: List (system)"
	permGC           = "Garbage collection: List (system)"
	permAuditLog     = "Audit log: List (system)"
	permJobQueues    = "Jobservice monitor: List (system)"
	permRobots       = "Robot account: List (system)"
)

// CheckHealth backs "Save & test". It checks that the server is reachable
// and is Harbor and that the credentials are accepted, says how many
// projects the account can see, and lists the areas it may not read.
func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	fail := func(err error) (*backend.CheckHealthResult, error) {
		p := d.explain(err)
		backend.Logger.Warn("Harbor health check failed", "status", int(p.status), "error", logSafe(p.message))
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: p.message}, nil
	}
	if d.settingsErr != nil {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: d.explain(d.settingsErr).message}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	// Harbor answers this without a login, so a refusal comes from
	// something else in front of it.
	info, err := d.client.SystemInfo(ctx)
	switch {
	case errors.Is(err, harbor.ErrUnauthorized), errors.Is(err, harbor.ErrForbidden):
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: "The server at " + d.settings.URL + " asked for a login before answering Harbor's system information, which Harbor itself never does. Check the URL. If a proxy in front of Harbor requires its own login, Grafana cannot get through it with a Harbor account.",
		}, nil
	case err != nil:
		return fail(err)
	}

	// Harbor treats a wrong password like no login on many endpoints and
	// shows the public projects. The statistics need a login, whatever the
	// account may read, so they tell whether the credentials are accepted.
	if d.client.HasCredentials() {
		if _, err := d.client.Statistics(ctx); err != nil {
			return fail(err)
		}
	}

	projects, err := d.client.Projects(ctx)
	if err != nil {
		return fail(needs(err, "projects", permProjects))
	}

	where := "Harbor"
	if info.Version != "" {
		where += " " + info.Version
	}
	who := "The account " + d.settings.Username
	if !d.client.HasCredentials() {
		who = "Anonymous access"
	}
	msg := fmt.Sprintf("Connected to %s. %s can see %d project%s.", where, who, len(projects), plural(len(projects)))
	if !d.client.HasCredentials() {
		msg += " Without a login only public projects, repositories and artifacts are readable. Enter a robot account to see more."
		return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: msg}, nil
	}
	if info.ReadOnly {
		msg += " Harbor is in read-only mode."
	}

	// The remaining areas each need their own permission. Missing ones do
	// not fail the check: the other queries still work.
	var missing []string
	probe := func(what, permission string, call func() error) {
		if err := call(); errors.Is(err, harbor.ErrForbidden) || errors.Is(err, harbor.ErrUnauthorized) {
			missing = append(missing, what+" ("+permission+")")
		}
	}
	probe("project quotas", permQuotas, func() error { _, err := d.client.Quotas(ctx); return err })
	probe("the registry's disk", permVolumes, func() error { _, err := d.client.Volumes(ctx); return err })
	probe("vulnerabilities", permSecurityHub, func() error { _, err := d.client.SecuritySummary(ctx); return err })
	probe("replications", permReplication, func() error {
		_, _, err := d.client.ReplicationExecutions(ctx, 0, "", 1)
		return err
	})
	probe("garbage collection", permGC, func() error { _, _, err := d.client.GCHistory(ctx, 1); return err })
	probe("job queues", permJobQueues, func() error { _, err := d.client.JobQueues(ctx); return err })
	probe("robot accounts", permRobots, func() error { _, err := d.client.Robots(ctx); return err })
	if len(missing) > 0 {
		msg += " Not readable with this login: " + strings.Join(missing, "; ") + "."
	}
	return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: msg}, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
