// Copyright IBM Corp. 2025
// SPDX-License-Identifier: MPL-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/go-tfe"
	"github.com/hashicorp/terraform-mcp-server/pkg/client"
	"github.com/hashicorp/terraform-mcp-server/pkg/utils"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	log "github.com/sirupsen/logrus"
)

const maxTaskDetailBytes = 5 << 20

type taskResultDetails struct {
	TaskResultID string                     `json:"task_result_id"`
	TaskResult   json.RawMessage            `json:"task_result"`
	Outcomes     []taskResultOutcomeSummary `json:"outcomes"`
	Pagination   json.RawMessage            `json:"outcomes_pagination,omitempty"`
}

// GetTaskResultDetails reads one task execution result.
func GetTaskResultDetails(logger *log.Logger) server.ServerTool {
	return server.ServerTool{
		Tool: mcp.NewTool("get_task_result_details",
			mcp.WithDescription("Get a task execution result and its outcome details, including available outcome bodies. Outcomes are paginated and large bodies may be omitted with an explicit status."),
			mcp.WithTitleAnnotation("Get task result details"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithString("task_result_id", mcp.Required(), mcp.Description("ID of the task result to read")),
			utils.WithPagination(),
		),
		Handler: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return getTaskResultDetailsHandler(ctx, req, logger)
		},
	}
}

func getTaskResultDetails(ctx context.Context, tfeClient *tfe.Client, id string, page, pageSize int) (*taskResultDetails, error) {
	data, err := readOutcomeResponse(ctx, tfeClient, "task-results/"+id)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				OutcomeCount *int `json:"task-result-outcomes-count"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode task result: %w", err)
	}
	if payload.Data.ID != id {
		return nil, fmt.Errorf("task result ID mismatch")
	}
	result := &taskResultDetails{TaskResultID: id, TaskResult: json.RawMessage(data), Outcomes: []taskResultOutcomeSummary{}}
	outcomes, err := listTaskResultOutcomes(ctx, tfeClient, id, page, pageSize)
	if err != nil {
		if strings.Contains(err.Error(), "404") && (payload.Data.Attributes.OutcomeCount == nil || *payload.Data.Attributes.OutcomeCount == 0) {
			return result, nil
		}
		return nil, fmt.Errorf("list task result outcomes: %w", err)
	}
	result.Outcomes = outcomes.Outcomes
	result.Pagination, err = json.Marshal(outcomes.Pagination)
	if err != nil {
		return nil, fmt.Errorf("encode outcome pagination: %w", err)
	}
	for index := range result.Outcomes {
		if result.Outcomes[index].BodyUploaded == nil || !*result.Outcomes[index].BodyUploaded {
			result.Outcomes[index].BodyStatus = "not_uploaded"
		} else {
			result.Outcomes[index].BodyStatus = "omitted_size_limit"
		}
	}
	baseline, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode task result metadata: %w", err)
	}
	if len(baseline) > maxTaskDetailBytes {
		return nil, fmt.Errorf("task result metadata exceeds maximum response size")
	}
	remaining := maxTaskDetailBytes - len(baseline)
	for index := range result.Outcomes {
		outcome := &result.Outcomes[index]
		if outcome.BodyUploaded == nil || !*outcome.BodyUploaded {
			continue
		}
		if remaining <= 0 {
			continue
		}
		body, err := getTaskResultOutcomeBodyLimit(ctx, tfeClient, outcome.ID, remaining)
		if err != nil {
			if strings.Contains(err.Error(), "too large") {
				outcome.BodyStatus = "omitted_size_limit"
				continue
			}
			outcome.BodyStatus = "unavailable"
			continue
		}
		outcome.Body = body
		outcome.BodyStatus = "included"
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("encode task result details: %w", err)
		}
		if len(encoded) > maxTaskDetailBytes {
			outcome.Body = ""
			outcome.BodyStatus = "omitted_size_limit"
			continue
		}
		remaining = maxTaskDetailBytes - len(encoded)
	}
	return result, nil
}

func getTaskResultDetailsHandler(ctx context.Context, request mcp.CallToolRequest, logger *log.Logger) (*mcp.CallToolResult, error) {
	id, err := request.RequireString("task_result_id")
	if err != nil || !taskResultIDPattern.MatchString(id) {
		return ToolErrorf(logger, "invalid task_result_id")
	}
	params, err := utils.OptionalPaginationParams(request)
	if err != nil || params.Page < 1 || params.PageSize < 1 || params.PageSize > 100 {
		return ToolErrorf(logger, "invalid pagination parameters: page must be >= 1 and pageSize between 1 and 100")
	}
	tfeClient, err := client.GetTfeClientFromContext(ctx, logger)
	if err != nil {
		return ToolError(logger, "failed to get Terraform client", err)
	}
	result, err := getTaskResultDetails(ctx, tfeClient, id, params.Page, params.PageSize)
	if err != nil {
		return ToolError(logger, "failed to get task result details", err)
	}
	output, err := json.Marshal(result)
	if err != nil {
		return ToolError(logger, "failed to encode task result details", err)
	}
	if len(output) > maxTaskDetailBytes {
		return ToolErrorf(logger, "task result details exceed maximum response size")
	}
	return mcp.NewToolResultText(string(output)), nil
}
