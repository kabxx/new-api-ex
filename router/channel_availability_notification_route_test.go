package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelAvailabilityNotificationTestRouteRequiresRootAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	found := false
	for _, route := range engine.Routes() {
		if route.Method == http.MethodPost && route.Path == "/api/option/channel-availability-notification/test" {
			found = true
			break
		}
	}
	require.True(t, found)

	request := httptest.NewRequest(http.MethodPost, "/api/option/channel-availability-notification/test", strings.NewReader(`{"recipients":["admin@example.com"]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	assert.NotEqual(t, http.StatusOK, response.Code)
}

func TestOptionBulkRouteRequiresRootAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	found := false
	for _, route := range engine.Routes() {
		if route.Method == http.MethodPut && route.Path == "/api/option/bulk" {
			found = true
			break
		}
	}
	require.True(t, found)

	request := httptest.NewRequest(http.MethodPut, "/api/option/bulk", strings.NewReader(`{"options":{"RetryTimes":"10"}}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	assert.NotEqual(t, http.StatusOK, response.Code)
}
