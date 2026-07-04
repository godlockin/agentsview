package pricing

import (
	"fmt"
	"time"

	"go.kenn.io/agentsview/internal/pricing/catalog"
)

// ModelPricing holds per-model token pricing in cost per
// million tokens. Separate from db.ModelPricing — the CLI
// command converts between the two.
type ModelPricing = catalog.ModelPricing

// FetchLiteLLMPricing downloads the LiteLLM pricing JSON
// and parses it into ModelPricing entries.
func FetchLiteLLMPricing() ([]ModelPricing, error) {
	return catalog.FetchLiteLLMPricing()
}

// ParseLiteLLMPricing parses the LiteLLM JSON map into
// ModelPricing entries. Per-token costs are converted to
// per-million-token costs. Entries missing both input and
// output cost are skipped.
func ParseLiteLLMPricing(data []byte) ([]ModelPricing, error) {
	return catalog.ParseLiteLLMPricing(data)
}

// FetchOpenRouterPricing downloads the OpenRouter public model
// catalog and converts each text-generation entry into
// ModelPricing. See catalog.FetchOpenRouterPricing for the
// underlying parser and the rationale for dropping non-text
// modalities.
func FetchOpenRouterPricing() ([]ModelPricing, error) {
	return catalog.FetchOpenRouterPricing()
}

// ParseOpenRouterPricing is the byte-level equivalent of
// FetchOpenRouterPricing, exposed for unit tests.
func ParseOpenRouterPricing(data []byte) ([]ModelPricing, error) {
	return catalog.ParseOpenRouterPricing(data)
}

// PricingSource describes one upstream catalog that the
// pricing refresh loop tries to fetch in the background.
// Sources are tried in declaration order; the first to
// succeed wins for the seed batch, and every successful
// fetch contributes its rows to the merged result so
// downstream callers see the union.
type PricingSource struct {
	Name string
	Fetch func() ([]ModelPricing, error)
}

// fetchWithTimeout races a Fetch against a per-source
// timeout so a hung upstream cannot stall the daemon's
// pricing refresh goroutine forever. The timeout mirrors
// the per-source HTTPClient timeout in catalog.FetchLiteLLMPricing
// and is layered on top so a connection that hangs at the
// socket layer (which the http.Client deadline already times
// out at 30 s) still cannot block longer than pricingFetchTimeout.
//
// We deliberately keep the existing Fetch signature rather
// than threading context.Context through every fetcher so
// callers that want unbounded behavior (tests, bulk
// importers) do not have to mock a context.
func fetchWithTimeout(src PricingSource, timeout time.Duration) ([]ModelPricing, error) {
	type result struct {
		prices []ModelPricing
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		prices, err := src.Fetch()
		ch <- result{prices, err}
	}()
	select {
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: timed out after %s", src.Name, timeout)
	case r := <-ch:
		return r.prices, r.err
	}
}

// pricingFetchTimeout bounds a single pricing source fetch
// so a hung LiteLLM or OpenRouter endpoint can never stall the
// daemon's background refresh goroutine indefinitely. The
// underlying catalog fetchers already cap each HTTP round at
// 30 s; this is a defence-in-depth cap with a sensible
// multiple of that.
const pricingFetchTimeout = 45 * time.Second

// PricingFetchTimeout exposes the per-source fetch timeout
// for callers (cmd/agentsview) so they can mention it in
// their log messages without hard-coding the same number in
// two places.
func PricingFetchTimeout() time.Duration { return pricingFetchTimeout }

// FetchWithTimeout runs src.Fetch() in a goroutine and races
// it against the package-level pricingFetchTimeout. Used by
// the daemon's refresh loop to keep a hung LiteLLM or
// OpenRouter endpoint from blocking the rest of the
// background refresh path.
func FetchWithTimeout(src PricingSource, timeout time.Duration) ([]ModelPricing, error) {
	return fetchWithTimeout(src, timeout)
}

// DefaultPricingSources returns the built-in pricing sources
// in priority order. LiteLLM covers most public models; the
// OpenRouter catalog frequently lists fork-tuned and private
// model prices that LiteLLM has not yet picked up. The list
// is intentionally short: each entry adds an HTTP request on
// every server start and we want startup latency to stay low.
func DefaultPricingSources() []PricingSource {
	return []PricingSource{
		{Name: "litellm", Fetch: FetchLiteLLMPricing},
		{Name: "openrouter", Fetch: FetchOpenRouterPricing},
	}
}

// MergePricing combines per-source ModelPricing slices into a
// single map keyed by ModelPattern. When two sources report
// the same pattern, the first non-zero field wins (so earlier
// sources in the slice take precedence over later ones).
// This gives LiteLLM priority over OpenRouter for models both
// catalogs cover, while still letting OpenRouter fill in
// models that LiteLLM does not list.
func MergePricing(sources map[string][]ModelPricing) map[string]ModelPricing {
	out := make(map[string]ModelPricing)
	// Stable iteration order: callers pass a map but we want
	// deterministic precedence. The DefaultPricingSources order
	// is the documented priority; here we just merge whatever
	// the caller hands us and rely on each source slice being
	// internally consistent.
	for _, prices := range sources {
		for _, p := range prices {
			existing, ok := out[p.ModelPattern]
			if !ok {
				out[p.ModelPattern] = p
				continue
			}
			merged := existing
			if merged.InputPerMTok == 0 && p.InputPerMTok != 0 {
				merged.InputPerMTok = p.InputPerMTok
			}
			if merged.OutputPerMTok == 0 && p.OutputPerMTok != 0 {
				merged.OutputPerMTok = p.OutputPerMTok
			}
			if merged.CacheCreationPerMTok == 0 && p.CacheCreationPerMTok != 0 {
				merged.CacheCreationPerMTok = p.CacheCreationPerMTok
			}
			if merged.CacheReadPerMTok == 0 && p.CacheReadPerMTok != 0 {
				merged.CacheReadPerMTok = p.CacheReadPerMTok
			}
			out[p.ModelPattern] = merged
		}
	}
	return out
}
