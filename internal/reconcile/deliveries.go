package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/StorScale/storscale-platform/internal/pipelines"
	"github.com/StorScale/storscale-platform/internal/project"
)

// A ProjectDelivery is one of a project's approved deliveries (use case
// 9.1), as the platform's DAG file (storscale_deliveries.py, in every
// Airflow) makes a DAG of it: in the project's own Airflow (Own), or the
// shared one. Its target and key are the Airflow connection ConnID.
type ProjectDelivery struct {
	Project   string           `json:"project"`
	Catalog   string           `json:"catalog"`
	Namespace string           `json:"namespace"`
	Own       bool             `json:"own"`
	ConnID    string           `json:"connId"`
	Delivery  project.Delivery `json:"delivery"`
}

// DeliveryConnID is the Airflow connection of a delivery's target and key.
func DeliveryConnID(p *project.Project, d project.Delivery) string {
	return "delivery_" + strings.ReplaceAll(p.Metadata.Name, "-", "_") + "_" + d.Name
}

// Deliveries are the applied projects' approved deliveries: an unapproved
// one has no DAG, so it doesn't run.
func (r *Reconciler) Deliveries(ctx context.Context) ([]ProjectDelivery, error) {
	statuses, err := r.Store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	out := []ProjectDelivery{}
	for _, st := range statuses {
		p := st.Spec
		if p == nil || st.Phase == project.Deleting || p.Spec.Tables == nil {
			continue
		}
		ready := false
		for _, pt := range st.Parts {
			ready = ready || pt.System == "deliveries" && pt.OK
		}
		if !ready {
			continue
		}
		for _, d := range p.Spec.Deliveries {
			if d.Approved() {
				out = append(out, ProjectDelivery{Project: p.Metadata.Name, Catalog: p.Spec.Tables.Catalog, Namespace: p.Spec.Tables.Namespace,
					Own: r.ownAirflow(p), ConnID: DeliveryConnID(p, d), Delivery: d})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnID < out[j].ConnID })
	return out, nil
}

// applyDeliveries gives the Airflow the project's DAGs run in each
// delivery's target and the recipient's key (one connection, its Extra the
// connection's keys), and takes away those of deliveries gone.
func (r *Reconciler) applyDeliveries(ctx context.Context, p *project.Project, before *project.Project) error {
	api, err := r.projectAirflowAPI(ctx, p)
	if err != nil {
		return err
	}
	var errs []error
	want := map[string]bool{}
	for _, d := range p.Spec.Deliveries {
		target, err := r.Store.Connection(ctx, p.Metadata.Name, d.Target)
		if err == nil && target == nil {
			err = fmt.Errorf("no connection %q among the project's", d.Target)
		}
		extra := map[string]any{"target": target}
		if err == nil && d.PGP != "" {
			var key map[string]string
			key, err = r.Store.Connection(ctx, p.Metadata.Name, d.PGP)
			if err == nil && key["pgpPublicKey"] == "" {
				err = fmt.Errorf("the connection %q has no pgpPublicKey", d.PGP)
			}
			extra["pgpPublicKey"] = key["pgpPublicKey"]
		}
		if err == nil {
			b, _ := json.Marshal(extra)
			err = api.PutConnection(ctx, pipelines.Connection{ID: DeliveryConnID(p, d), Type: "generic", Extra: string(b),
				Description: "The delivery " + d.Name + " of the project " + p.Metadata.Name + ": its target and key (the platform keeps it)"})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("delivery %s: %w", d.Name, err))
			continue
		}
		want[DeliveryConnID(p, d)] = true
	}
	if before != nil {
		for _, d := range before.Spec.Deliveries {
			if id := DeliveryConnID(before, d); !want[id] {
				errs = append(errs, api.DeleteConnection(ctx, id))
			}
		}
	}
	return errors.Join(errs...)
}

// removeDeliveries takes a deleted project's deliveries' connections from the
// shared Airflow (a project's own Airflow goes with it, connections and all).
func (r *Reconciler) removeDeliveries(ctx context.Context, p *project.Project) error {
	if r.Airflow == nil || r.ownAirflow(p) {
		return nil
	}
	var errs []error
	for _, d := range p.Spec.Deliveries {
		errs = append(errs, r.Airflow.API.DeleteConnection(ctx, DeliveryConnID(p, d)))
	}
	return errors.Join(errs...)
}
