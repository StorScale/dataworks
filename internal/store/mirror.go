package store

import (
	"context"
	"errors"

	"github.com/StorScale/storscale-platform/internal/project"
)

// Mirror is a Store (Kube) with a copy of every project and status kept in a
// bucket, as Bucket keeps them, for the services that read projects there
// (semanticd, catalog-sync, the setup steps). Reads come from Store; the
// copy is written alongside, and brought in step on every Projects.
type Mirror struct {
	Store
	Copy Store // a Bucket
}

func (m *Mirror) Projects(ctx context.Context) ([]*project.Project, error) {
	ps, err := m.Store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	copies, err := m.Copy.Projects(ctx)
	if err != nil {
		return ps, err
	}
	live := map[string]bool{}
	var errs []error
	for _, p := range ps {
		live[p.Metadata.Name] = true
		errs = append(errs, m.Copy.PutProject(ctx, p))
	}
	for _, c := range copies {
		if !live[c.Metadata.Name] {
			errs = append(errs, m.Copy.DeleteProject(ctx, c.Metadata.Name))
		}
	}
	return ps, errors.Join(errs...)
}

func (m *Mirror) PutProject(ctx context.Context, p *project.Project) error {
	if err := m.Store.PutProject(ctx, p); err != nil {
		return err
	}
	return m.Copy.PutProject(ctx, p)
}

func (m *Mirror) DeleteProject(ctx context.Context, name string) error {
	if err := m.Store.DeleteProject(ctx, name); err != nil {
		return err
	}
	return m.Copy.DeleteProject(ctx, name)
}

// Statuses are Store's, and the copy's of projects Store no longer has: one
// deleted before the operator could undo it (made without its finalizer),
// whose access the operator must still take out.
func (m *Mirror) Statuses(ctx context.Context) ([]*project.Status, error) {
	sts, err := m.Store.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	copies, err := m.Copy.Statuses(ctx)
	if err != nil {
		return sts, nil // the copy's are extra: Store's are the ones that count
	}
	seen := map[string]bool{}
	for _, st := range sts {
		seen[st.Name] = true
	}
	for _, c := range copies {
		if seen[c.Name] {
			continue
		}
		if p, err := m.Store.Project(ctx, c.Name); err == nil && p == nil {
			sts = append(sts, c) // gone from Store, yet not undone
		}
	}
	return sts, nil
}

func (m *Mirror) PutStatus(ctx context.Context, st *project.Status) error {
	if err := m.Store.PutStatus(ctx, st); err != nil {
		return err
	}
	return m.Copy.PutStatus(ctx, st)
}

func (m *Mirror) DeleteStatus(ctx context.Context, name string) error {
	if err := m.Store.DeleteStatus(ctx, name); err != nil {
		return err
	}
	return m.Copy.DeleteStatus(ctx, name)
}

// Open is the store a platform's configuration names: kind "bucket" (Bucket,
// at endpoint) or "kubernetes" (Kube, mirrored into the bucket at endpoint
// when one is given).
func Open(kind, endpoint, accessKey, secretKey, bucket string) (Store, error) {
	var b *Bucket
	if endpoint != "" {
		var err error
		if b, err = New(endpoint, accessKey, secretKey, bucket); err != nil {
			return nil, err
		}
	}
	switch kind {
	case "", "bucket":
		if b == nil {
			return nil, errors.New("the project store is a bucket, but there's no STORE_URL")
		}
		return b, nil
	case "kubernetes":
		k, err := NewKube()
		if err != nil {
			return nil, err
		}
		if b == nil {
			return k, nil
		}
		return &Mirror{Store: k, Copy: b}, nil
	}
	return nil, errors.New("PROJECT_STORE is bucket or kubernetes, not " + kind)
}
