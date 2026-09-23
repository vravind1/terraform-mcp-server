// Copyright IBM Corp. 2025
// SPDX-License-Identifier: MPL-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/go-tfe"
	"github.com/hashicorp/terraform-mcp-server/pkg/utils"
	"github.com/mark3labs/mcp-go/mcp"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type fakeTaskStages struct {
	tfe.TaskStages
	list     *tfe.TaskStageList
	read     map[string]*tfe.TaskStage
	listErr  error
	readErr  error
	listOpts *tfe.TaskStageListOptions
	readOpts *tfe.TaskStageReadOptions
	reads    int
}

func (f *fakeTaskStages) List(_ context.Context, _ string, opts *tfe.TaskStageListOptions) (*tfe.TaskStageList, error) {
	f.listOpts = opts
	return f.list, f.listErr
}

func (f *fakeTaskStages) Read(_ context.Context, id string, opts *tfe.TaskStageReadOptions) (*tfe.TaskStage, error) {
	f.reads++
	f.readOpts = opts
	return f.read[id], f.readErr
}

func TestTaskResultToolsMetadata(t *testing.T) {
	for _, tc := range []struct {
		tool     mcp.Tool
		name     string
		required string
	}{
		{ListTaskResults(log.New()).Tool, "list_task_results", "run_id"},
		{GetTaskResultDetails(log.New()).Tool, "get_task_result_details", "task_result_id"},
	} {
		require.Equal(t, tc.name, tc.tool.Name)
		require.Contains(t, tc.tool.InputSchema.Required, tc.required)
		require.True(t, *tc.tool.Annotations.ReadOnlyHint)
		require.False(t, *tc.tool.Annotations.DestructiveHint)
	}
	require.Contains(t, ListTaskResults(log.New()).Tool.InputSchema.Properties, "page")
	require.Contains(t, ListTaskResults(log.New()).Tool.InputSchema.Properties, "pageSize")
	require.Contains(t, GetTaskResultDetails(log.New()).Tool.InputSchema.Properties, "page")
	require.Contains(t, GetTaskResultDetails(log.New()).Tool.InputSchema.Properties, "pageSize")
}

func TestListTaskResultsHydratesStages(t *testing.T) {
	fake := &fakeTaskStages{
		list: &tfe.TaskStageList{Pagination: &tfe.Pagination{CurrentPage: 2, NextPage: 3}, Items: []*tfe.TaskStage{
			{ID: "ts-one", Stage: tfe.PostPlan, Status: tfe.TaskStageRunning, TaskResults: []*tfe.TaskResult{{ID: "taskrs-one"}}},
			{ID: "ts-two", Stage: tfe.PreApply, Status: tfe.TaskStagePending},
		}},
		read: map[string]*tfe.TaskStage{"ts-one": {
			ID: "ts-one", Status: tfe.TaskStageFailed, TaskResults: []*tfe.TaskResult{
				{ID: "taskrs-one", TaskName: "native-check", Status: tfe.TaskFailed, Message: "denied", WorkspaceTaskEnforcementLevel: tfe.Mandatory},
				{ID: "taskrs-two", TaskName: "integration-check", Status: tfe.TaskPassed, Message: "ok"},
			},
		}},
	}
	got, err := listTaskResults(context.Background(), fake, "run-one", utils.PaginationParams{Page: 2, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 2, fake.listOpts.PageNumber)
	require.Equal(t, 10, fake.listOpts.PageSize)
	require.Equal(t, []tfe.TaskStageIncludeOpt{tfe.TaskStageTaskResults}, fake.readOpts.Include)
	require.Equal(t, 1, fake.reads)
	require.Equal(t, "run-one", got.RunID)
	require.Equal(t, 3, got.NextPage)
	require.Len(t, got.Stages, 2)
	require.Equal(t, tfe.TaskStageFailed, got.Stages[0].Status)
	require.Equal(t, "denied", got.Stages[0].TaskResults[0].Message)
	require.Equal(t, tfe.TaskPassed, got.Stages[0].TaskResults[1].Status)
	require.Empty(t, got.Stages[1].TaskResults)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"task_name":"native-check"`)
}

func TestListTaskResultsEmptyAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fake    *fakeTaskStages
		wantErr bool
	}{
		{"no stages", &fakeTaskStages{list: &tfe.TaskStageList{}}, false},
		{"list fails", &fakeTaskStages{listErr: errors.New("not authorized")}, true},
		{"read fails", &fakeTaskStages{list: &tfe.TaskStageList{Items: []*tfe.TaskStage{{ID: "ts-one", TaskResults: []*tfe.TaskResult{{ID: "taskrs-one"}}}}}, readErr: errors.New("not found")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listTaskResults(context.Background(), tc.fake, "run-one", utils.PaginationParams{Page: 1})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got.Stages)
			require.Empty(t, got.Stages)
		})
	}
}

func TestGetTaskResultDetails(t *testing.T) {
	api := taskOutcomeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/ping" {
			_, _ = w.Write([]byte(`{"meta":{"rate-limit":{"limit":30,"remaining":30,"reset":60}}}`))
			return
		}
		switch r.URL.Path {
		case "/api/v2/task-results/taskrs-one":
			_, _ = w.Write([]byte(`{"data":{"id":"taskrs-one","type":"task-results","attributes":{"task-name":"native-check","status":"failed","message":"denied","task-category":"native","task-result-outcomes-count":2}}}`))
		case "/api/v2/task-results/taskrs-one/outcomes":
			_, _ = w.Write([]byte(`{"data":[{"id":"taskrso-one","type":"task-result-outcomes","attributes":{"outcome-id":"cost_estimates","body-uploaded":true}},{"id":"taskrso-two","type":"task-result-outcomes","attributes":{"outcome-id":"task-error","body-uploaded":null}}],"meta":{"pagination":{"current-page":1,"next-page":0}}}`))
		case "/api/v2/task-result-outcomes/taskrso-one/body":
			_, _ = w.Write([]byte(`{"result":{"cost":63.07}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	got, err := getTaskResultDetails(context.Background(), api, "taskrs-one", 1, 10)
	require.NoError(t, err)
	require.Equal(t, "taskrs-one", got.TaskResultID)
	require.Contains(t, string(got.TaskResult), `"task-category":"native"`)
	require.Contains(t, string(got.TaskResult), `"status":"failed"`)
	require.Len(t, got.Outcomes, 2)
	require.Equal(t, "cost_estimates", got.Outcomes[0].OutcomeID)
	require.Equal(t, `{"result":{"cost":63.07}}`, got.Outcomes[0].Body)
	require.Equal(t, "included", got.Outcomes[0].BodyStatus)
	require.Equal(t, "not_uploaded", got.Outcomes[1].BodyStatus)
	require.Contains(t, string(got.Pagination), `"current-page":1`)
	_, err = getTaskResultDetails(context.Background(), api, "taskrs-missing", 1, 10)
	require.Error(t, err)
}

func TestGetTaskResultDetailsUnavailableBodyAndNoOutcomes(t *testing.T) {
	api := taskOutcomeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/ping" {
			_, _ = w.Write([]byte(`{"meta":{"rate-limit":{"limit":30,"remaining":30,"reset":60}}}`))
			return
		}
		switch r.URL.Path {
		case "/api/v2/task-results/taskrs-one":
			_, _ = w.Write([]byte(`{"data":{"id":"taskrs-one","type":"task-results","attributes":{"status":"failed"}}}`))
		case "/api/v2/task-results/taskrs-one/outcomes":
			_, _ = w.Write([]byte(`{"data":[{"id":"taskrso-one","attributes":{"outcome-id":"recommendations","body-uploaded":true}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	got, err := getTaskResultDetails(context.Background(), api, "taskrs-one", 1, 10)
	require.NoError(t, err)
	require.Equal(t, "unavailable", got.Outcomes[0].BodyStatus)
}

func TestGetTaskResultDetailsWithoutOutcomes(t *testing.T) {
	api := taskOutcomeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/ping" {
			_, _ = w.Write([]byte(`{"meta":{"rate-limit":{"limit":30,"remaining":30,"reset":60}}}`))
			return
		}
		if r.URL.Path == "/api/v2/task-results/taskrs-one" {
			_, _ = w.Write([]byte(`{"data":{"id":"taskrs-one","type":"task-results","attributes":{"status":"passed","task-result-outcomes-count":0}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	got, err := getTaskResultDetails(context.Background(), api, "taskrs-one", 1, 10)
	require.NoError(t, err)
	require.Empty(t, got.Outcomes)
}

func TestGetTaskResultDetailsLimitsCombinedBody(t *testing.T) {
	api := taskOutcomeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/ping" {
			_, _ = w.Write([]byte(`{"meta":{"rate-limit":{"limit":30,"remaining":30,"reset":60}}}`))
			return
		}
		switch r.URL.Path {
		case "/api/v2/task-results/taskrs-one":
			_, _ = w.Write([]byte(`{"data":{"id":"taskrs-one","type":"task-results","attributes":{"status":"failed"}}}`))
		case "/api/v2/task-results/taskrs-one/outcomes":
			_, _ = w.Write([]byte(`{"data":[{"id":"taskrso-one","attributes":{"outcome-id":"cost_estimates","body-uploaded":true}},{"id":"taskrso-two","attributes":{"outcome-id":"policy_evals","body-uploaded":true}}]}`))
		default:
			_, _ = w.Write([]byte(strings.Repeat("x", maxTaskDetailBytes/2)))
		}
	}))
	got, err := getTaskResultDetails(context.Background(), api, "taskrs-one", 1, 10)
	require.NoError(t, err)
	require.Equal(t, "included", got.Outcomes[0].BodyStatus)
	require.Equal(t, "omitted_size_limit", got.Outcomes[1].BodyStatus)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), maxTaskDetailBytes)
}

func TestTaskResultHandlerValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]interface{}
		message string
	}{
		{"missing run", nil, "run_id"},
		{"empty run", map[string]interface{}{"run_id": "  "}, "run_id"},
		{"invalid page", map[string]interface{}{"run_id": "run-one", "page": float64(-1)}, "pagination"},
		{"invalid size", map[string]interface{}{"run_id": "run-one", "pageSize": float64(101)}, "pagination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := mcp.CallToolRequest{}
			request.Params.Arguments = tc.args
			got, err := listTaskResultsHandler(context.Background(), request, log.New())
			require.NoError(t, err)
			require.True(t, got.IsError)
			require.Contains(t, got.Content[0].(mcp.TextContent).Text, tc.message)
		})
	}
	request := mcp.CallToolRequest{}
	got, err := getTaskResultDetailsHandler(context.Background(), request, log.New())
	require.NoError(t, err)
	require.True(t, got.IsError)
	require.Contains(t, got.Content[0].(mcp.TextContent).Text, "task_result_id")
}
