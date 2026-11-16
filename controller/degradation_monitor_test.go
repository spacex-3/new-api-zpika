package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withDegradationMonitorOptions(t *testing.T, monitorURL string, userVisible bool) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	common.OptionMap = map[string]string{
		degradationMonitorURLKey:         monitorURL,
		degradationMonitorUserVisibleKey: map[bool]string{true: "true", false: "false"}[userVisible],
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previous
		common.OptionMapRWMutex.Unlock()
	})
}

func TestValidateDegradationMonitorURL(t *testing.T) {
	for _, value := range []string{"", "https://monitor.example.com/embed", "https://monitor.example.com/embed?mode=compact"} {
		assert.NoError(t, validateDegradationMonitorURL(value), value)
	}
	for _, value := range []string{"http://monitor.example.com/embed", "https://user:pass@monitor.example.com/embed", "https://monitor.example.com/embed#secret", "not-a-url"} {
		assert.Error(t, validateDegradationMonitorURL(value), value)
	}
}

func TestGetDegradationMonitorEnforcesVisibilityWithoutExposingURLInStatus(t *testing.T) {
	withDegradationMonitorOptions(t, "https://monitor.example.com/embed", false)

	statusResponse := httptest.NewRecorder()
	statusContext, _ := gin.CreateTestContext(statusResponse)
	statusContext.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	GetStatus(statusContext)
	assert.NotContains(t, statusResponse.Body.String(), "monitor.example.com")
	assert.Contains(t, statusResponse.Body.String(), `"degradation_monitor_configured":true`)

	userResponse := httptest.NewRecorder()
	userContext, _ := gin.CreateTestContext(userResponse)
	userContext.Set("role", common.RoleCommonUser)
	GetDegradationMonitor(userContext)
	assert.Equal(t, http.StatusForbidden, userResponse.Code)

	adminResponse := httptest.NewRecorder()
	adminContext, _ := gin.CreateTestContext(adminResponse)
	adminContext.Set("role", common.RoleAdminUser)
	GetDegradationMonitor(adminContext)
	require.Equal(t, http.StatusOK, adminResponse.Code)
	assert.Contains(t, adminResponse.Body.String(), "https://monitor.example.com/embed")
}

func TestGetDegradationMonitorAllowsVisibleUser(t *testing.T) {
	withDegradationMonitorOptions(t, "https://monitor.example.com/embed", true)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Set("role", common.RoleCommonUser)

	GetDegradationMonitor(context)

	require.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"success":true,"message":"","data":{"configured":true,"url":"https://monitor.example.com/embed"}}`, response.Body.String())
}

func TestGetDegradationMonitorHidesStaleVisibilityWithoutURL(t *testing.T) {
	withDegradationMonitorOptions(t, "", true)

	statusResponse := httptest.NewRecorder()
	statusContext, _ := gin.CreateTestContext(statusResponse)
	statusContext.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	GetStatus(statusContext)
	assert.Contains(t, statusResponse.Body.String(), `"degradation_monitor_configured":false`)
	assert.Contains(t, statusResponse.Body.String(), `"degradation_monitor_user_visible":false`)

	userResponse := httptest.NewRecorder()
	userContext, _ := gin.CreateTestContext(userResponse)
	userContext.Set("role", common.RoleCommonUser)
	GetDegradationMonitor(userContext)
	assert.Equal(t, http.StatusForbidden, userResponse.Code)
}
