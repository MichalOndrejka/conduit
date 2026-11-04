// Discovery of an Azure DevOps project's work item types, so a source picks
// from the types its project's process actually defines (Agile, Scrum, CMMI,
// Basic or a custom inherited process) instead of a fixed list.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// adoHiddenTypeCategory is the category Azure DevOps files its internal work
// item types under (Test Plan, Test Suite, Shared Steps, Code Review and
// Feedback types): ones the process defines but its boards never show.
const adoHiddenTypeCategory = "Microsoft.HiddenCategory"

// AdoWorkItemTypes lists the work item types of src's Azure DevOps project
// (AdoOrg, AdoProject, AdoApiVersion and the Password credential), sorted by
// name, leaving out disabled types and the hidden category's.
func AdoWorkItemTypes(ctx context.Context, src *models.SourceDefinition, store secrets.Reader) ([]string, error) {
	org := strings.TrimRight(strings.TrimSpace(src.GetConfig("AdoOrg")), "/")
	project := strings.TrimSpace(src.GetConfig("AdoProject"))
	if org == "" || project == "" {
		return nil, fmt.Errorf("an Azure DevOps organization URL and project are required")
	}
	apiVersion := strings.TrimSpace(src.GetConfig("AdoApiVersion"))
	if apiVersion == "" {
		apiVersion = "7.1"
	}
	a := &APISource{src: src, secrets: store}
	client := a.httpClient()
	base := org + "/" + url.PathEscape(project) + "/_apis/wit/"
	query := "?api-version=" + url.QueryEscape(apiVersion)

	data, err := a.getRaw(ctx, client, base+"workitemtypes"+query)
	if err != nil {
		return nil, fmt.Errorf("listing work item types: %w", err)
	}
	var types struct {
		Value []struct {
			Name       string `json:"name"`
			IsDisabled bool   `json:"isDisabled"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &types); err != nil {
		return nil, fmt.Errorf("work item types response is not valid JSON: %w", err)
	}

	data, err = a.getRaw(ctx, client, base+"workitemtypecategories"+query)
	if err != nil {
		return nil, fmt.Errorf("listing work item type categories: %w", err)
	}
	var categories struct {
		Value []struct {
			ReferenceName string `json:"referenceName"`
			WorkItemTypes []struct {
				Name string `json:"name"`
			} `json:"workItemTypes"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &categories); err != nil {
		return nil, fmt.Errorf("work item type categories response is not valid JSON: %w", err)
	}
	hidden := map[string]bool{}
	for _, c := range categories.Value {
		if c.ReferenceName == adoHiddenTypeCategory {
			for _, t := range c.WorkItemTypes {
				hidden[t.Name] = true
			}
		}
	}

	names := make([]string, 0, len(types.Value))
	for _, t := range types.Value {
		if t.Name != "" && !t.IsDisabled && !hidden[t.Name] {
			names = append(names, t.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}
