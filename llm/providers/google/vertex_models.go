package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// Vertex publisher-model discovery (spec 2026-09-04-google-vertex-express-and-
// discovery §2.3-§2.4). The listing is served by ModelGardenService on
// v1beta1 at a host-relative path with no project or location — it does not
// exist on the project-scoped v1 base URL the overlay builds — and it
// returns ids and a launch stage only, no capability data. So the URL is
// composed from the base URL's scheme and host, and "is this a text model"
// is a heuristic on the id, pinned by testdata/vertex_publisher_models.json.

// vertexListingVersion is the API version that serves the listing.
const vertexListingVersion = "/v1beta1"

// vertexPageSize is the page size requested; today's listing fits in one page.
const vertexPageSize = "200"

// vertexTextModelDenylist names the modalities the listing mixes in with
// text generation; an id containing any of them is dropped.
var vertexTextModelDenylist = []string{"tts", "embedding", "image", "live", "transcribe", "translate", "omni", "audio"}

// vertexLaunchStages are the stages a listed id must carry to be offered.
var vertexLaunchStages = map[string]bool{"GA": true, "PUBLIC_PREVIEW": true}

type vertexPublisherModel struct {
	Name        string `json:"name"` // publishers/google/models/<id>
	LaunchStage string `json:"launchStage"`
}

type vertexPublisherModelsPage struct {
	PublisherModels []vertexPublisherModel `json:"publisherModels"`
	NextPageToken   string                 `json:"nextPageToken"`
}

// isVertexTransport reports whether res reaches Vertex: the overlay's
// vertex-location host rule is the discriminator, not the URL's shape.
func isVertexTransport(res registry.Resolved) bool {
	return res.Transport.HostRule == registry.HostRuleVertexLocation
}

// vertexModelsURL is scheme://host of the resolved base URL + /v1beta1 +
// the transport's models endpoint, with the page query.
func vertexModelsURL(res registry.Resolved, pageToken string) (string, error) {
	u, err := url.Parse(res.Transport.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("models.list: vertex base URL %q has no scheme or host", res.Transport.BaseURL)
	}
	q := url.Values{"pageSize": {vertexPageSize}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	return u.Scheme + "://" + u.Host + vertexListingVersion + res.Transport.ModelsEndpoint + "?" + q.Encode(), nil
}

// vertexTextModel is the spec §2.4 filter.
func vertexTextModel(id, launchStage string) bool {
	if !strings.HasPrefix(id, "gemini-") || !vertexLaunchStages[launchStage] {
		return false
	}
	for _, token := range vertexTextModelDenylist {
		if strings.Contains(id, token) {
			return false
		}
	}
	return true
}

// filterVertexModels keeps the text-generation Gemini rows, in listing
// order, as bare registry rows: the catalog supplies metadata for known ids.
func filterVertexModels(entries []vertexPublisherModel) []registry.Model {
	var rows []registry.Model
	for _, m := range entries {
		id := m.Name[strings.LastIndex(m.Name, "/")+1:]
		if vertexTextModel(id, m.LaunchStage) {
			rows = append(rows, registry.Model{ID: id})
		}
	}
	return rows
}

// listVertexModels fetches every page and filters.
func (p *Protocol) listVertexModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	var entries []vertexPublisherModel
	pageToken := ""
	for {
		u, err := vertexModelsURL(res, pageToken)
		if err != nil {
			return nil, err
		}
		var page vertexPublisherModelsPage
		err = protocolhttp.Do(ctx, p.call("models.list", "google_models", http.MethodGet, u, nil, llm.Request{Model: "*"}, res), func(r *protocolhttp.Result) (*llm.Response, error) {
			if err := json.Unmarshal(r.Body, &page); err != nil {
				return nil, fmt.Errorf("models.list: %w", err)
			}
			return nil, nil
		})
		if err != nil {
			return nil, err
		}
		entries = append(entries, page.PublisherModels...)
		if page.NextPageToken == "" {
			return filterVertexModels(entries), nil
		}
		pageToken = page.NextPageToken
	}
}
