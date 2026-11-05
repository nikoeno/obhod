package pc

import (
	"fmt"

	"github.com/nikoejento/obhod/internal/pc/sites"
)

// Per-site rules (user-approved 2026-11-01; HTTP side in api/desktop). The rules
// live in internal/pc/sites (Paths.Data/sites.json). This file wires them to
// the checker: the rules are handed to checker.SetOverrides at start and after
// every change. The checker publishes the new table, and the reload loop that
// already reacts to tables applies it (routes event at once, processes after the
// 3 s debounce while connected), so a rule needs no restart.

// SiteRule is one per-site rule: Domain (normalized, covers subdomains), Action
// (direct | desync | tunnel | block) and AddedAt.
type SiteRule = sites.Rule

// The error kinds of the site calls (errors.Is). None holds a value of the input.
var (
	ErrSiteDomain   = sites.ErrInvalidDomain // not a plain domain name (URL, port, IP, wildcard, empty)
	ErrSiteAction   = sites.ErrInvalidAction // not direct, desync, tunnel or block
	ErrSiteLimit    = sites.ErrLimit         // 500 rules stored
	ErrSiteNotFound = sites.ErrNotFound      // delete: no rule for that domain
)

// siteStore opens the rule store on first use (New touches no file).
func (a *App) siteStore() (*sites.Store, error) {
	a.siteMu.Lock()
	defer a.siteMu.Unlock()
	return a.siteStoreLocked()
}

func (a *App) siteStoreLocked() (*sites.Store, error) {
	if a.sites == nil {
		st, err := sites.Open(a.p.Data, sites.OSFS())
		if err != nil {
			return nil, err
		}
		if st.Recovered() {
			a.log.Warn("the site rules file was not valid; it was moved aside and the rules start empty", "file", sites.CorruptName)
		}
		a.sites = st
	}
	return a.sites, nil
}

// loadSiteRules gives the checker the stored rules before its first table. A
// rules file that cannot be read is a warning: the app starts without rules.
func (a *App) loadSiteRules() {
	a.siteMu.Lock()
	defer a.siteMu.Unlock()
	st, err := a.siteStoreLocked()
	if err != nil {
		a.log.Warn("site rules not loaded", "err", err)
		return
	}
	if err := a.d.checker.SetOverrides(st.Entries()); err != nil {
		a.log.Warn("site rules not applied", "err", err)
	}
}

// Sites lists the rules sorted by domain; never nil.
func (a *App) Sites() ([]SiteRule, error) {
	st, err := a.siteStore()
	if err != nil {
		return nil, err
	}
	return st.List(), nil
}

// SetSite adds or changes the rule of a domain (upsert) and applies it: the
// checker rebuilds its table with the rule (highest precedence; a domain in no
// list is added), and the reload path updates the config of sing-box (and the
// desync engine) while connected. It does not wait for a connect or reload.
// created is true for a new rule. Errors: ErrSiteDomain, ErrSiteAction,
// ErrSiteLimit, or an internal one (the file could not be written).
func (a *App) SetSite(domain, action string) (rule SiteRule, created bool, err error) {
	a.siteMu.Lock()
	defer a.siteMu.Unlock()
	st, err := a.siteStoreLocked()
	if err != nil {
		return SiteRule{}, false, err
	}
	rule, created, err = st.Set(domain, action)
	if err != nil {
		return SiteRule{}, false, err
	}
	if err := a.d.checker.SetOverrides(st.Entries()); err != nil {
		return SiteRule{}, false, fmt.Errorf("apply site rules: %w", err)
	}
	a.log.Info("site rule set", "created", created, "rules", len(st.List()))
	return rule, created, nil
}

// DeleteSite removes the rule of a domain: the domain goes back to automatic
// (its stored decision or the default if it is in the candidate list, its
// static list entry if any; a domain in no list leaves the route table).
// Errors: ErrSiteDomain, ErrSiteNotFound.
func (a *App) DeleteSite(domain string) error {
	a.siteMu.Lock()
	defer a.siteMu.Unlock()
	st, err := a.siteStoreLocked()
	if err != nil {
		return err
	}
	if err := st.Delete(domain); err != nil {
		return err
	}
	if err := a.d.checker.SetOverrides(st.Entries()); err != nil {
		return fmt.Errorf("apply site rules: %w", err)
	}
	a.log.Info("site rule deleted", "rules", len(st.List()))
	return nil
}
