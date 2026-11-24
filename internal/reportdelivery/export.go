package reportdelivery

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FindingExport is the JSON shape of the single-vulnerability export.
type FindingExport struct {
	SpecVersion int     `json:"spec_version"`
	Meta        Meta    `json:"meta"`
	Finding     Finding `json:"finding"`
}

// RenderFindingJSON renders the single-vulnerability export as JSON.
func RenderFindingJSON(meta Meta, f Finding) ([]byte, error) {
	f = SanitizeFinding(f)
	return json.MarshalIndent(FindingExport{
		SpecVersion: SpecVersion,
		Meta:        meta,
		Finding:     f,
	}, "", "  ")
}

// FindingTitle builds the SRC submission title: owner/system + vulnerability
// type + location. It never invents a classification that is not present.
func FindingTitle(meta Meta, f Finding) string {
	owner := firstNonEmpty(meta.Company, meta.TaskName, strings.Join(nonEmpty(meta.Targets), "、"), f.Target)
	title := valueOr(f.Title, "未命名漏洞")
	if owner != "" && !strings.Contains(title, owner) {
		title = owner + " " + title
	}
	location := firstNonEmpty(f.Endpoint, f.Target)
	if location != "" && !strings.Contains(title, location) {
		title = fmt.Sprintf("%s（%s）", title, location)
	}
	return title
}
