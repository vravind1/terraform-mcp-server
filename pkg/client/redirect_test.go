// Copyright IBM Corp. 2025
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestRedirectDropsCredentialsAcrossOrigins(t *testing.T) {
	final := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get(SharedSecretHeader))
		require.Empty(t, r.Header.Get("X-Forwarded-For"))
		_, _ = w.Write([]byte("downloaded"))
	}))
	defer final.Close()
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		w.Header().Set("Location", final.URL+"/body")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	httpClient := CreateHTTPClient(true, log.New())
	request, err := http.NewRequest(http.MethodGet, first.URL, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set(SharedSecretHeader, "deployment-secret")
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	response, err := httpClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "downloaded", string(body))
}

func TestRedirectRejectsInsecureTarget(t *testing.T) {
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1:1/body")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	response, err := CreateHTTPClient(true, log.New()).Get(first.URL)
	if response != nil {
		response.Body.Close()
	}
	require.ErrorContains(t, err, "insecure cross-origin redirect")
}

func TestRedirectDoesNotForwardSignedURLAsReferer(t *testing.T) {
	final := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Referer"))
		_, _ = w.Write([]byte("downloaded"))
	}))
	defer final.Close()
	middle := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", final.URL+"/body")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer middle.Close()
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", middle.URL+"/signed?signature=private")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	response, err := CreateHTTPClient(true, log.New()).Get(first.URL)
	require.NoError(t, err)
	defer response.Body.Close()
}

func TestRedirectFailureDoesNotLogSignedURL(t *testing.T) {
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://127.0.0.1:1/signed?signature=private")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	var logs bytes.Buffer
	logger := log.New()
	logger.SetOutput(&logs)
	response, err := CreateHTTPClient(true, logger).Get(first.URL)
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err)
	require.NotContains(t, logs.String(), "signature=private")
}
