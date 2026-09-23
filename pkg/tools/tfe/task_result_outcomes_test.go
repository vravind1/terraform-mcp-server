// Copyright IBM Corp. 2025
// SPDX-License-Identifier: MPL-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/go-tfe"
	"github.com/hashicorp/terraform-mcp-server/pkg/client"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func taskOutcomeClient(t *testing.T, handler http.Handler) *tfe.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	api, err := tfe.NewClient(&tfe.Config{Address: server.URL, Token: "test-token", HTTPClient: client.CreateHTTPClient(true, log.New())})
	require.NoError(t, err)
	return api
}

func TestListTaskResultOutcomes(t *testing.T) {
	api := taskOutcomeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/ping" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"meta":{"rate-limit":{"limit":30,"remaining":30,"reset":60}}}`))
			return
		}
		require.Equal(t, "/api/v2/task-results/taskrs-example/outcomes", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "2", r.URL.Query().Get("page[number]"))
		require.Equal(t, "1", r.URL.Query().Get("page[size]"))
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[{"id":"taskrso-resource","type":"task-result-outcomes","attributes":{"outcome-id":"cost_estimates","description":"Cost","body-uploaded":true,"tags":[{"label":"currency_code","value":[{"label":"USD","level":"none"}]}]}}],"meta":{"pagination":{"current-page":2,"next-page":3,"page-size":1}}}`))
	}))
	got, err := listTaskResultOutcomes(context.Background(), api, "taskrs-example", 2, 1)
	require.NoError(t, err)
	require.Len(t, got.Outcomes, 1)
	require.Equal(t, "taskrso-resource", got.Outcomes[0].ID)
	require.Equal(t, "cost_estimates", got.Outcomes[0].OutcomeID)
	require.NotNil(t, got.Outcomes[0].BodyUploaded)
	require.True(t, *got.Outcomes[0].BodyUploaded)
	require.Equal(t, 3, got.Pagination.NextPage)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "test-token")
}

func TestGetTaskResultOutcomeBody(t *testing.T) {
	blob := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get(client.SharedSecretHeader))
		require.Empty(t, r.Header.Get("X-Forwarded-For"))
		_, _ = w.Write([]byte(`{"meta":{},"result":{"cost":63.07}}`))
	}))
	defer blob.Close()
	api := taskOutcomeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/ping" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"meta":{"rate-limit":{"limit":30,"remaining":30,"reset":60}}}`))
			return
		}
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "/api/v2/task-result-outcomes/taskrso-example/body", r.URL.Path)
		w.Header().Set("Location", blob.URL+"/signed")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	got, err := getTaskResultOutcomeBody(context.Background(), api, "taskrso-example")
	require.NoError(t, err)
	require.JSONEq(t, `{"meta":{},"result":{"cost":63.07}}`, got)
}

func TestGetTaskResultOutcomeBodyRejectsOversizedAndMissing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"missing", http.StatusNotFound, "", "404"},
		{"oversized", http.StatusOK, strings.Repeat("x", maxOutcomeBodyBytes+1), "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := taskOutcomeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/ping" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"meta":{"rate-limit":{"limit":30,"remaining":30,"reset":60}}}`))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			_, err := getTaskResultOutcomeBody(context.Background(), api, "taskrso-example")
			require.ErrorContains(t, err, tc.want)
		})
	}
}
