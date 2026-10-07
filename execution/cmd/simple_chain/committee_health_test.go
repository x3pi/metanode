package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
)

func TestCommitteeKeyHealthAndEndpoints(t *testing.T) {
	app := &App{}

	// 1. Initial / default status
	status, warn := app.CommitteeKeyStatus()
	assert.Equal(t, "unknown", status)
	assert.Empty(t, warn)

	// 2. Test "ok" status
	app.setCommitteeKeyStatus("ok", "")
	metrics.ValidatorCommitteeKeyValid.Set(1)
	status, warn = app.CommitteeKeyStatus()
	assert.Equal(t, "ok", status)
	assert.Empty(t, warn)
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.ValidatorCommitteeKeyValid))

	// 3. Test "mismatch" status with warning
	app.setCommitteeKeyStatus("mismatch", "attestation key mismatch with on-chain PublicKeyBls")
	metrics.ValidatorCommitteeKeyValid.Set(0)
	status, warn = app.CommitteeKeyStatus()
	assert.Equal(t, "mismatch", status)
	assert.Equal(t, "attestation key mismatch with on-chain PublicKeyBls", warn)
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ValidatorCommitteeKeyValid))

	// 4. Test "not_validator" status
	app.setCommitteeKeyStatus("not_validator", "")
	metrics.ValidatorCommitteeKeyValid.Set(-1)
	status, warn = app.CommitteeKeyStatus()
	assert.Equal(t, "not_validator", status)
	assert.Empty(t, warn)
	assert.Equal(t, float64(-1), testutil.ToFloat64(metrics.ValidatorCommitteeKeyValid))

	// 5. Test JSON Snapshot from MetricsCollector
	mc := NewMetricsCollector(app)
	snapshot := mc.Snapshot()
	rollupSection, ok := snapshot["rollup"].(map[string]interface{})
	require.True(t, ok, "snapshot must contain 'rollup' section")
	assert.Equal(t, "not_validator", rollupSection["committee_key_status"])

	// 6. Test /health handler with committee_key
	healthHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"status": "ok",
		}
		if app != nil {
			keyStatus, wMsg := app.CommitteeKeyStatus()
			resp["committee_key"] = keyStatus
			if wMsg != "" {
				resp["committee_key_warning"] = wMsg
			}
		}
		json.NewEncoder(w).Encode(resp)
	})

	app.setCommitteeKeyStatus("ok", "")
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	healthHandler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var healthResp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &healthResp))
	assert.Equal(t, "ok", healthResp["committee_key"])

	// 7. Test /readiness handler: mismatch must mark readiness = false
	readinessHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ready := true
		checks := map[string]string{
			"db": "ok",
		}
		if app != nil {
			keyStatus, wMsg := app.CommitteeKeyStatus()
			checks["committee_key"] = keyStatus
			if keyStatus == "mismatch" {
				ready = false
				checks["committee_key_warning"] = wMsg
			}
		}
		resp := map[string]interface{}{
			"ready":  ready,
			"checks": checks,
		}
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		json.NewEncoder(w).Encode(resp)
	})

	// OK status -> readiness 200 OK
	app.setCommitteeKeyStatus("ok", "")
	recOK := httptest.NewRecorder()
	readinessHandler.ServeHTTP(recOK, req)
	assert.Equal(t, http.StatusOK, recOK.Code)

	// Mismatch status -> readiness 503 Service Unavailable
	app.setCommitteeKeyStatus("mismatch", "key mismatch")
	recMismatch := httptest.NewRecorder()
	readinessHandler.ServeHTTP(recMismatch, req)
	assert.Equal(t, http.StatusServiceUnavailable, recMismatch.Code)
	var readyResp map[string]interface{}
	require.NoError(t, json.Unmarshal(recMismatch.Body.Bytes(), &readyResp))
	assert.Equal(t, false, readyResp["ready"])
	checksMap := readyResp["checks"].(map[string]interface{})
	assert.Equal(t, "mismatch", checksMap["committee_key"])
	assert.Equal(t, "key mismatch", checksMap["committee_key_warning"])
}
