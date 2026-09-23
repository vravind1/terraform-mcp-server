// Copyright IBM Corp. 2025
// SPDX-License-Identifier: MPL-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/go-tfe"
)

const maxOutcomeBodyBytes = 5 << 20

var taskResultIDPattern = regexp.MustCompile(`^taskrs-[a-zA-Z0-9]+$`)
var outcomeIDPattern = regexp.MustCompile(`^taskrso-[a-zA-Z0-9]+$`)

type taskResultOutcome struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		OutcomeID    string          `json:"outcome-id"`
		Description  string          `json:"description"`
		Tags         json.RawMessage `json:"tags"`
		URL          string          `json:"url"`
		BodyUploaded *bool           `json:"body-uploaded"`
	} `json:"attributes"`
}

type taskResultOutcomeSummary struct {
	ID           string          `json:"id"`
	OutcomeID    string          `json:"outcome_id"`
	Description  string          `json:"description,omitempty"`
	Tags         json.RawMessage `json:"tags,omitempty"`
	URL          string          `json:"url,omitempty"`
	BodyUploaded *bool           `json:"body_uploaded,omitempty"`
	Body         string          `json:"body,omitempty"`
	BodyStatus   string          `json:"body_status"`
}

type taskResultOutcomeList struct {
	TaskResultID string                     `json:"task_result_id"`
	Outcomes     []taskResultOutcomeSummary `json:"outcomes"`
	Pagination   struct {
		CurrentPage int `json:"current-page"`
		NextPage    int `json:"next-page"`
		PageSize    int `json:"page-size"`
		TotalCount  int `json:"total-count"`
		TotalPages  int `json:"total-pages"`
	} `json:"pagination"`
}

func readOutcomeResponse(ctx context.Context, tfeClient *tfe.Client, path string) ([]byte, error) {
	return readOutcomeResponseLimit(ctx, tfeClient, path, maxOutcomeBodyBytes)
}

func readOutcomeResponseLimit(ctx context.Context, tfeClient *tfe.Client, path string, limit int) ([]byte, error) {
	req, err := tfeClient.NewRequest("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("create outcome request: %w", err)
	}
	body, err := req.DoRaw(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, fmt.Errorf("outcome unavailable (404)")
		}
		return nil, fmt.Errorf("outcome request failed")
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read outcome: %w", err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("outcome body too large (maximum %d bytes)", limit)
	}
	return data, nil
}

func listTaskResultOutcomes(ctx context.Context, tfeClient *tfe.Client, id string, page, pageSize int) (*taskResultOutcomeList, error) {
	path := fmt.Sprintf("task-results/%s/outcomes?page%%5Bnumber%%5D=%d&page%%5Bsize%%5D=%d", id, page, pageSize)
	data, err := readOutcomeResponse(ctx, tfeClient, path)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []taskResultOutcome `json:"data"`
		Meta struct {
			Pagination json.RawMessage `json:"pagination"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode task result outcomes: %w", err)
	}
	result := &taskResultOutcomeList{TaskResultID: id, Outcomes: make([]taskResultOutcomeSummary, 0, len(payload.Data))}
	if len(payload.Meta.Pagination) > 0 {
		if err := json.Unmarshal(payload.Meta.Pagination, &result.Pagination); err != nil {
			return nil, fmt.Errorf("decode task result outcome pagination: %w", err)
		}
	}
	for _, item := range payload.Data {
		result.Outcomes = append(result.Outcomes, taskResultOutcomeSummary{
			ID: item.ID, OutcomeID: item.Attributes.OutcomeID, Description: item.Attributes.Description,
			Tags: item.Attributes.Tags, URL: item.Attributes.URL, BodyUploaded: item.Attributes.BodyUploaded,
		})
	}
	return result, nil
}

func getTaskResultOutcomeBody(ctx context.Context, tfeClient *tfe.Client, id string) (string, error) {
	return getTaskResultOutcomeBodyLimit(ctx, tfeClient, id, maxOutcomeBodyBytes)
}

func getTaskResultOutcomeBodyLimit(ctx context.Context, tfeClient *tfe.Client, id string, limit int) (string, error) {
	data, err := readOutcomeResponseLimit(ctx, tfeClient, "task-result-outcomes/"+id+"/body", limit)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("outcome body is not UTF-8 text")
	}
	return string(data), nil
}
