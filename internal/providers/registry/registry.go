// Package registry lists the integrations and resolves a source to the
// configuration its fetcher reads, through the source's provider.
package registry

import (
	"fmt"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
	"github.com/MichalOndrejka/conduit/internal/providers/ado"
	"github.com/MichalOndrejka/conduit/internal/providers/github"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// all lists every integration, in the order the UI offers them.
var all = []providers.Integration{ado.Integration{}, github.Integration{}}

// All returns every integration.
func All() []providers.Integration { return all }

// Get returns the integration of the given kind.
func Get(kind string) (providers.Integration, bool) {
	for _, p := range all {
		if p.Kind() == kind {
			return p, true
		}
	}
	return nil, false
}

// ProviderReader looks up saved providers (store.ProviderStore).
type ProviderReader interface {
	Get(id string) (*models.Provider, error)
	ListAll() ([]models.Provider, error)
}

// Resolver turns sources into what the fetchers in internal/sources read.
// It is also the secrets.Reader those fetchers get: a compiled source names
// its provider's token entry (models.ProviderTokenName), which the secrets
// store holds.
type Resolver struct {
	Providers ProviderReader
	Secrets   secrets.Reader
}

// GetValue implements secrets.Reader.
func (r *Resolver) GetValue(name string) string {
	if r.Secrets == nil {
		return ""
	}
	return r.Secrets.GetValue(name)
}

// Provider returns a saved provider and its integration.
func (r *Resolver) Provider(id string) (*models.Provider, providers.Integration, error) {
	if id == "" {
		return nil, nil, fmt.Errorf("no provider given")
	}
	p, err := r.Providers.Get(id)
	if err != nil {
		return nil, nil, fmt.Errorf("reading provider: %w", err)
	}
	if p == nil {
		return nil, nil, fmt.Errorf("no provider has id %q", id)
	}
	in, ok := Get(p.Kind)
	if !ok {
		return nil, nil, fmt.Errorf("provider %q has unknown kind %q", p.Name, p.Kind)
	}
	return p, in, nil
}

// Conn resolves a provider's token.
func (r *Resolver) Conn(p *models.Provider) providers.Conn {
	return providers.Conn{Provider: *p, Token: r.GetValue(models.ProviderTokenName(p.ID))}
}

// Effective returns a copy of src whose Config is what its fetcher reads.
func (r *Resolver) Effective(src *models.SourceDefinition) (*models.SourceDefinition, error) {
	if src.ProviderID == "" {
		return nil, fmt.Errorf("source %q has no provider — sources created before providers existed must be recreated", src.Name)
	}
	p, in, err := r.Provider(src.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", src.Name, err)
	}
	out := *src
	out.Config = in.Compile(*p, src)
	return &out, nil
}

// EffectiveAll compiles every source that resolves; the others are skipped
// (a source whose provider was deleted can't be read).
func (r *Resolver) EffectiveAll(all []models.SourceDefinition) []models.SourceDefinition {
	out := make([]models.SourceDefinition, 0, len(all))
	for i := range all {
		if eff, err := r.Effective(&all[i]); err == nil {
			out = append(out, *eff)
		}
	}
	return out
}

// SourceLister lists stored sources (store.SourceConfigStore).
type SourceLister interface {
	ListAll() ([]models.SourceDefinition, error)
}

// EffectiveLister lists sources compiled for their fetchers — for the live
// tools, which read sources through internal/sources directly.
type EffectiveLister struct {
	Sources  SourceLister
	Resolver *Resolver
}

func (l EffectiveLister) ListAll() ([]models.SourceDefinition, error) {
	all, err := l.Sources.ListAll()
	if err != nil {
		return nil, err
	}
	return l.Resolver.EffectiveAll(all), nil
}
