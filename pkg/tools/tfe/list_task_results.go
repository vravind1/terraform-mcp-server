// Copyright IBM Corp. 2025
// SPDX-License-Identifier: MPL-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/go-tfe"
	"github.com/hashicorp/terraform-mcp-server/pkg/client"
	"github.com/hashicorp/terraform-mcp-server/pkg/utils"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	log "github.com/sirupsen/logrus"
)

// ListTaskResults lists execution results for tasks in one Terraform run.
func ListTaskResults(logger *log.Logger) server.ServerTool {
	return server.ServerTool{
		Tool: mcp.NewTool("list_task_results",
			mcp.WithDescription("List task execution results for a Terraform run, including stage and individual task statuses and messages."),
			mcp.WithTitleAnnotation("List task results for a Terraform run"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithString("run_id", mcp.Required(), mcp.Description("ID of the run whose task results to list")),
			utils.WithPagination(),
		),
		Handler: func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return listTaskResultsHandler(ctx, req, logger)
		},
	}
}

type taskResultSummary struct {
	ID               string                         `json:"id"`
	TaskID           string                         `json:"task_id"`
	TaskName         string                         `json:"task_name"`
	Status           tfe.TaskResultStatus           `json:"status"`
	Message          string                         `json:"message"`
	URL              string                         `json:"url"`
	EnforcementLevel tfe.TaskEnforcementLevel       `json:"enforcement_level"`
	StatusTimestamps tfe.TaskResultStatusTimestamps `json:"status_timestamps"`
	CreatedAt        time.Time                      `json:"created_at"`
	UpdatedAt        time.Time                      `json:"updated_at"`
}

type taskStageSummary struct {
	ID          string              `json:"id"`
	Stage       tfe.Stage           `json:"stage"`
	Status      tfe.TaskStageStatus `json:"status"`
	TaskResults []taskResultSummary `json:"task_results"`
}

type taskResultsForRun struct {
	RunID  string             `json:"run_id"`
	Stages []taskStageSummary `json:"stages"`
	*tfe.Pagination
}

func listTaskResults(ctx context.Context, stages tfe.TaskStages, runID string, pagination utils.PaginationParams) (*taskResultsForRun, error) {
	list, err := stages.List(ctx, runID, &tfe.TaskStageListOptions{
		ListOptions: tfe.ListOptions{PageNumber: pagination.Page, PageSize: pagination.PageSize},
	})
	if err != nil {
		return nil, fmt.Errorf("list task stages for run %s: %w", runID, err)
	}

	result := &taskResultsForRun{RunID: runID, Stages: make([]taskStageSummary, 0, len(list.Items)), Pagination: list.Pagination}
	for _, stage := range list.Items {
		if stage == nil {
			continue
		}
		entry := taskStageSummary{ID: stage.ID, Stage: stage.Stage, Status: stage.Status, TaskResults: []taskResultSummary{}}
		if len(stage.TaskResults) > 0 {
			// Listing stages returns relationship IDs, not populated result attributes.
			full, err := stages.Read(ctx, stage.ID, &tfe.TaskStageReadOptions{Include: []tfe.TaskStageIncludeOpt{tfe.TaskStageTaskResults}})
			if err != nil {
				return nil, fmt.Errorf("read task stage %s for run %s: %w", stage.ID, runID, err)
			}
			entry.Status = full.Status
			for _, task := range full.TaskResults {
				if task == nil {
					continue
				}
				entry.TaskResults = append(entry.TaskResults, taskResultSummary{
					ID: task.ID, TaskID: task.TaskID, TaskName: task.TaskName,
					Status: task.Status, Message: task.Message, URL: task.URL,
					EnforcementLevel: task.WorkspaceTaskEnforcementLevel,
					StatusTimestamps: task.StatusTimestamps, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
				})
			}
		}
		result.Stages = append(result.Stages, entry)
	}
	return result, nil
}

func listTaskResultsHandler(ctx context.Context, request mcp.CallToolRequest, logger *log.Logger) (*mcp.CallToolResult, error) {
	runID, err := request.RequireString("run_id")
	if err != nil || strings.TrimSpace(runID) == "" {
		return ToolError(logger, "missing required input: run_id", err)
	}
	pagination, err := utils.OptionalPaginationParams(request)
	if err != nil {
		return ToolError(logger, "invalid pagination parameters", err)
	}
	if pagination.Page < 1 || pagination.PageSize < 1 || pagination.PageSize > 100 {
		return ToolError(logger, "invalid pagination parameters", errors.New("page must be at least 1 and pageSize must be between 1 and 100"))
	}
	tfeClient, err := client.GetTfeClientFromContext(ctx, logger)
	if err != nil {
		return ToolError(logger, "failed to get Terraform client", err)
	}
	result, err := listTaskResults(ctx, tfeClient.TaskStages, runID, pagination)
	if err != nil {
		return ToolError(logger, "failed to list task results", err)
	}
	buf, err := json.Marshal(result)
	if err != nil {
		return ToolError(logger, "failed to marshal task results", err)
	}
	return mcp.NewToolResultText(string(buf)), nil
}
