package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestZtr_TokenCannotBeReused is a regression test for #474.
//
// Previously, the Ztr handler looked up the token with a plain SELECT
// (GetTokenByValue) and only deleted it at the very end of the handler,
// after several other DB/network calls had already run. That left a
// window where the same token could be submitted twice (e.g. a retry or
// a replay) and both requests would pass validation, each triggering a
// robot secret rotation.
//
// The fix replaces the lookup with an atomic "claim" (DELETE ... RETURNING)
// so a token can only ever be consumed once. This test simulates two
// requests using the same token: the first should succeed, the second
// must be rejected as invalid because the token no longer exists.
func TestZtr_TokenCannotBeReused(t *testing.T) {
	server, mock := newMockServer(t)

	token := "test-ztr-token-value"
	now := time.Now().UTC()
	expiresAt := now.Add(24 * time.Hour)

	tokenColumns := []string{"id", "satellite_id", "token", "created_at", "updated_at", "expires_at"}

	// --- First request: token is claimed successfully ---
	mock.ExpectQuery("DELETE FROM satellite_token").
		WithArgs(token).
		WillReturnRows(sqlmock.NewRows(tokenColumns).
			AddRow(1, int32(1), token, now, now, expiresAt))

	// Robot account lookup fails fast on purpose: we only need to prove the
	// token was consumed by ClaimToken, not exercise the entire success path
	// (which needs Harbor/state-artifact mocking out of scope for this test).
	mock.ExpectQuery("SELECT .+ FROM robot_accounts").
		WithArgs(int32(1)).
		WillReturnError(sql.ErrNoRows)

	body, err := json.Marshal(ZTRRequest{Token: token})
	require.NoError(t, err)

	req1 := httptest.NewRequest(http.MethodPost, "/api/ztr", bytes.NewReader(body))
	rr1 := httptest.NewRecorder()
	server.Ztr(rr1, req1)

	// We expect an internal error here (robot account not found), not an
	// "Invalid Token" error — proving the token itself was accepted and
	// consumed on this first call.
	require.Equal(t, http.StatusInternalServerError, rr1.Code)
	require.NotContains(t, rr1.Body.String(), "Invalid Token")

	// --- Second request with the SAME token: must now be rejected ---
	mock.ExpectQuery("DELETE FROM satellite_token").
		WithArgs(token).
		WillReturnError(sql.ErrNoRows)

	req2 := httptest.NewRequest(http.MethodPost, "/api/ztr", bytes.NewReader(body))
	rr2 := httptest.NewRecorder()
	server.Ztr(rr2, req2)

	require.Equal(t, http.StatusBadRequest, rr2.Code)
	require.Contains(t, rr2.Body.String(), "Invalid Token")

	require.NoError(t, mock.ExpectationsWereMet())
}
