package reconcile

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/StorScale/storscale-platform/internal/gitlab"
	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
	"github.com/StorScale/storscale-platform/internal/trino"
)

// Reviewer keeps each project's open merge requests' data branches (use case
// 7.3): a Nessie branch of the project's tables per merge request, a Trino
// catalog on it (made while Trino runs, as the operator's own account), and a
// run of the platform's DAG storscale_review in the project's Airflow at
// each new commit, which builds the change's dbt models there and tells the
// merge request. A merge request closed or merged, its branch and catalog
// go: production's own runs build what was merged.
type Reviewer struct {
	Trino  *trino.Client
	Nessie *Nessie
	// GitLab makes the client of a project's GitLab (tests give their own).
	GitLab func(base, token, project string) MergeRequests
}

// MergeRequests is what reviews need of GitLab.
type MergeRequests interface {
	OpenMergeRequests(ctx context.Context, target string) ([]gitlab.MergeRequest, error)
	Status(ctx context.Context, sha, state, name, description, target string) error
}

// ReviewDAG is the platform's DAG that reviews a merge request's commit.
const ReviewDAG = "storscale_review"

// openReviews are the project's open merge requests, as GitLab has them
// (none, with no error, when the project has no reviews).
func (r *Reconciler) openReviews(ctx context.Context, p *project.Project) ([]gitlab.MergeRequest, MergeRequests, error) {
	pl := p.Spec.Pipelines
	if r.Reviewer == nil || pl == nil || pl.Reviews == nil {
		return nil, nil, nil
	}
	secret, err := r.Store.Connection(ctx, p.Metadata.Name, pl.Credentials.SecretRef)
	if err != nil || secret == nil {
		return nil, nil, fmt.Errorf("the repo's credentials (%s): %v", pl.Credentials.SecretRef, err)
	}
	path := pl.Reviews.Project
	if path == "" {
		path = gitlab.ProjectPath(pl.Repo)
	}
	newClient := r.Reviewer.GitLab
	if newClient == nil {
		newClient = func(base, token, project string) MergeRequests { return gitlab.New(base, token, project) }
	}
	gl := newClient(pl.Reviews.GitLab, secret["password"], path)
	target := pl.Ref
	if target == "" {
		target = "main"
	}
	mrs, err := gl.OpenMergeRequests(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(mrs, func(i, j int) bool { return mrs[i].IID < mrs[j].IID })
	return mrs, gl, nil
}

func iids(mrs []gitlab.MergeRequest) []int {
	out := []int{}
	for _, m := range mrs {
		out = append(out, m.IID)
	}
	return out
}

// reviewCatalog is the statement making a branch's catalog: the lakehouse's
// own settings (stack/trino/catalog/iceberg.properties), at the branch.
func reviewCatalog(cat, branch string) string {
	return fmt.Sprintf(`CREATE CATALOG IF NOT EXISTS %s USING iceberg WITH (`+
		`"iceberg.catalog.type" = 'rest', "iceberg.rest-catalog.uri" = 'http://nessie:19120/iceberg/%s/', `+
		`"iceberg.rest-catalog.warehouse" = 'warehouse', "fs.s3.enabled" = 'true', "s3.endpoint" = 'http://buckets:9000', `+
		`"s3.region" = 'us-east-1', "s3.path-style-access" = 'true', `+
		`"s3.aws-access-key" = '${ENV:TRINO_S3_ACCESS_KEY}', "s3.aws-secret-key" = '${ENV:TRINO_S3_SECRET_KEY}')`,
		trino.Quote(cat), branch)
}

// applyReviews keeps the branches and catalogs of the open merge requests,
// and starts a review of each new commit of those seen at the last pass too
// (so Trino has its Ranger policies by then); it removes those of merge
// requests no longer open. before: the iids the operator kept last time.
func (r *Reconciler) applyReviews(ctx context.Context, p *project.Project, mrs []gitlab.MergeRequest, gl MergeRequests, before []int) error {
	rv := r.Reviewer
	var errs []error
	open := iids(mrs)
	for _, iid := range before {
		if slices.Contains(open, iid) {
			continue
		}
		if _, err := rv.Trino.Exec(ctx, "DROP CATALOG IF EXISTS "+trino.Quote(p.ReviewCatalog(iid))); err != nil {
			errs = append(errs, err)
		}
		if err := rv.Nessie.api().DropBranch(ctx, p.ReviewBranch(iid)); err != nil {
			errs = append(errs, err)
		}
	}
	if len(mrs) == 0 {
		return errors.Join(errs...)
	}
	api, err := r.projectAirflowAPI(ctx, p)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	pl := p.Spec.Pipelines
	path := pl.Reviews.Project
	if path == "" {
		path = gitlab.ProjectPath(pl.Repo)
	}
	for _, mr := range mrs {
		branch, cat := p.ReviewBranch(mr.IID), p.ReviewCatalog(mr.IID)
		if _, err := rv.Nessie.api().Branch(ctx, branch); err != nil {
			errs = append(errs, fmt.Errorf("merge request %d: its branch: %w", mr.IID, err))
			continue
		}
		if _, err := rv.Trino.Exec(ctx, reviewCatalog(cat, branch)); err != nil {
			errs = append(errs, fmt.Errorf("merge request %d: its catalog: %w", mr.IID, err))
			continue
		}
		if !slices.Contains(before, mr.IID) || mr.SHA == "" {
			continue // its policies reach Trino within its plugin's poll: the next pass starts it
		}
		runID := fmt.Sprintf("mr-%d-%.12s", mr.IID, mr.SHA)
		started, err := api.Trigger(ctx, ReviewDAG, runID, map[string]any{
			"project": p.Metadata.Name, "iid": mr.IID, "sha": mr.SHA, "branch": branch, "catalog": cat,
			"prodCatalog": p.Spec.Tables.Catalog, "namespace": p.Spec.Tables.Namespace, "dbt": pl.Reviews.Dbt,
			"gitlab": pl.Reviews.GitLab, "gitlabProject": path, "url": mr.WebURL})
		if err != nil {
			errs = append(errs, fmt.Errorf("merge request %d: its review: %w", mr.IID, err))
			continue
		}
		if started {
			if err := gl.Status(ctx, mr.SHA, "pending", "storscale/dbt", "a dbt build on the merge request's data branch", mr.WebURL); err != nil {
				errs = append(errs, fmt.Errorf("merge request %d: GitLab: %w", mr.IID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// projectAirflowAPI is the Airflow the project's DAGs run in: its own, or the shared one.
func (r *Reconciler) projectAirflowAPI(ctx context.Context, p *project.Project) (*pipelines.Client, error) {
	if r.ownAirflow(p) {
		return r.ProjectAirflows.API(ctx, p)
	}
	if r.Airflow == nil {
		return nil, errors.New("no Airflow here")
	}
	return r.Airflow.API, nil
}
