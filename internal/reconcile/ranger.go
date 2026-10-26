package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/StorScale/dataworks/internal/project"
)

// Ranger is Apache Ranger's admin API, for one service (Trino's).
type Ranger struct {
	URL, User, Password, Service string
}

func (r *Ranger) do(ctx context.Context, method, path string, body, out any) error {
	return call(ctx, method, r.URL+path, func(req *http.Request) { req.SetBasicAuth(r.User, r.Password) }, body, out)
}

type rangerPolicy struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// policies are the service's policies whose names start with prefix.
func (r *Ranger) policies(ctx context.Context, prefix string) (map[string]int, error) {
	var all []rangerPolicy
	if err := r.do(ctx, "GET", "/service/public/v2/api/service/"+url.PathEscape(r.Service)+"/policy", nil, &all); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, p := range all {
		if strings.HasPrefix(p.Name, prefix) {
			out[p.Name] = p.ID
		}
	}
	return out, nil
}

// syncPolicies makes the policies named with prefix exactly these.
func (r *Ranger) syncPolicies(ctx context.Context, prefix string, want []project.RangerPolicy) error {
	have, err := r.policies(ctx, prefix)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, p := range want {
		wanted[p.Name] = true
	}
	for name, id := range have {
		if !wanted[name] {
			if err := r.do(ctx, "DELETE", fmt.Sprintf("/service/public/v2/api/policy/%d", id), nil, nil); err != nil {
				return err
			}
		}
	}
	for _, p := range want {
		body := struct {
			project.RangerPolicy
			Service   string `json:"service"`
			IsEnabled bool   `json:"isEnabled"`
		}{p, r.Service, true}
		if id, ok := have[p.Name]; ok {
			err = r.do(ctx, "PUT", fmt.Sprintf("/service/public/v2/api/policy/%d", id), body, nil)
		} else {
			err = r.do(ctx, "POST", "/service/public/v2/api/policy", body, nil)
		}
		if err != nil {
			return fmt.Errorf("policy %s: %w", p.Name, err)
		}
	}
	return nil
}

// knownGroups drops from the policies' items the groups Ranger doesn't have,
// the items left naming no one, and the policies left with no items.
func knownGroups(policies []project.RangerPolicy, have map[string]int) []project.RangerPolicy {
	keep := func(items []project.RangerItem) []project.RangerItem {
		var out []project.RangerItem
		for _, it := range items {
			var gs []string
			for _, g := range it.Groups {
				if _, ok := have[g]; ok {
					gs = append(gs, g)
				}
			}
			if len(gs) == 0 && len(it.Users) == 0 {
				continue
			}
			it.Groups = gs
			out = append(out, it)
		}
		return out
	}
	out := make([]project.RangerPolicy, 0, len(policies))
	for _, p := range policies {
		p.PolicyItems = keep(p.PolicyItems)
		p.DataMaskPolicyItems = keep(p.DataMaskPolicyItems)
		p.RowFilterPolicyItems = keep(p.RowFilterPolicyItems)
		if len(p.PolicyItems)+len(p.DataMaskPolicyItems)+len(p.RowFilterPolicyItems) > 0 {
			out = append(out, p)
		}
	}
	return out
}

type rangerGroup struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (r *Ranger) groups(ctx context.Context) (map[string]int, error) {
	var res struct {
		Groups []rangerGroup `json:"vXGroups"`
	}
	if err := r.do(ctx, "GET", "/service/xusers/groups?pageSize=10000", nil, &res); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, g := range res.Groups {
		out[g.Name] = g.ID
	}
	return out, nil
}

// ensureGroups makes the groups that are missing, and returns every group's ID.
func (r *Ranger) ensureGroups(ctx context.Context, names []string) (map[string]int, error) {
	have, err := r.groups(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if _, ok := have[n]; !ok {
			var g rangerGroup
			if err := r.do(ctx, "POST", "/service/xusers/groups", map[string]string{"name": n, "description": "a StorScale DataWorks project's group"}, &g); err != nil {
				return nil, err
			}
			have[n] = g.ID
		}
	}
	return have, nil
}

func (r *Ranger) deleteGroups(ctx context.Context, names []string) error {
	have, err := r.groups(ctx)
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, ok := have[n]; ok {
			if err := r.do(ctx, "DELETE", "/service/xusers/secure/groups/"+url.PathEscape(n)+"?forceDelete=true", nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncGroupMembers sets, for every Ranger user, which of the managed groups
// they're in (want: username -> groups), leaving their other groups alone.
// The groups in want are made if they're missing; people Ranger doesn't know
// yet are added, as its usersync would.
func (r *Ranger) syncGroupMembers(ctx context.Context, managed []string, want map[string][]string, password string) error {
	var used []string
	for _, gs := range want {
		used = append(used, gs...)
	}
	ids, err := r.ensureGroups(ctx, used)
	if err != nil {
		return err
	}
	isManaged := map[int]bool{}
	for _, n := range managed {
		if id, ok := ids[n]; ok {
			isManaged[id] = true
		}
	}
	var res struct {
		Users []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			GroupIDList []int  `json:"groupIdList"`
		} `json:"vXUsers"`
	}
	if err := r.do(ctx, "GET", "/service/xusers/users?pageSize=10000", nil, &res); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, u := range res.Users {
		known[u.Name] = true
		var desired []int
		for _, g := range want[u.Name] {
			desired = append(desired, ids[g])
		}
		current := map[int]bool{}
		for _, id := range u.GroupIDList {
			if isManaged[id] {
				current[id] = true
			}
		}
		same := len(current) == len(desired)
		for _, id := range desired {
			same = same && current[id]
		}
		if same {
			continue
		}
		var full map[string]any
		if err := r.do(ctx, "GET", fmt.Sprintf("/service/xusers/secure/users/%d", u.ID), nil, &full); err != nil {
			return err
		}
		groupIDs := desired
		for _, id := range u.GroupIDList {
			if !isManaged[id] {
				groupIDs = append(groupIDs, id)
			}
		}
		full["groupIdList"] = groupIDs
		if err := r.do(ctx, "PUT", fmt.Sprintf("/service/xusers/secure/users/%d", u.ID), full, nil); err != nil {
			return fmt.Errorf("user %s: %w", u.Name, err)
		}
	}
	for name, groups := range want {
		if known[name] || len(groups) == 0 {
			continue
		}
		var groupIDs []int
		for _, g := range groups {
			groupIDs = append(groupIDs, ids[g])
		}
		if err := r.do(ctx, "POST", "/service/xusers/secure/users", map[string]any{
			"name": name, "firstName": name, "password": password + name, "userRoleList": []string{"ROLE_USER"},
			"groupIdList": groupIDs, "status": 1, "userSource": 1}, nil); err != nil {
			return fmt.Errorf("user %s: %w", name, err)
		}
	}
	return nil
}
