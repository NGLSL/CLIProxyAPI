package home

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const pluginStatusReportTimeout = 10 * time.Second

// PluginStatusClient defines the interface for pushing plugin status reports.
type PluginStatusClient interface {
	RPushPluginStatus(ctx context.Context, payload []byte) error
}

// ReportPluginStatus marshals the given report, sets node_id and updated_at,
// and pushes it to the provided client with a timeout.
func ReportPluginStatus(ctx context.Context, client PluginStatusClient, nodeID string, report any) error {
	if client == nil {
		return fmt.Errorf("home plugin status client is unavailable")
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return fmt.Errorf("home plugin status node id is empty")
	}
	rawReport, errMarshal := json.Marshal(report)
	if errMarshal != nil {
		return errMarshal
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(rawReport, &payload); errUnmarshal != nil {
		return errUnmarshal
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["node_id"] = nodeID
	payload["updated_at"] = time.Now().UTC()
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return errMarshal
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reportCtx, cancel := context.WithTimeout(ctx, pluginStatusReportTimeout)
	defer cancel()
	return client.RPushPluginStatus(reportCtx, raw)
}
