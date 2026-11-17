/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestValidateFeedbackPageURL(t *testing.T) {
	for _, value := range []string{"", "https://kf.example.com/embed", "https://kf.example.com/embed?site=vip"} {
		assert.NoError(t, validateFeedbackPageURL(value), value)
	}
	for _, value := range []string{"http://kf.example.com", "https://user:pass@kf.example.com", "https://kf.example.com/#fragment", "/embed"} {
		assert.Error(t, validateFeedbackPageURL(value), value)
	}
}

func TestGetFeedbackPageEnforcesUserVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	common.OptionMap = map[string]string{
		feedbackPageURLKey:         "https://kf.example.com/embed",
		feedbackPageUserVisibleKey: "false",
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previous
		common.OptionMapRWMutex.Unlock()
	})

	userRecorder := httptest.NewRecorder()
	userContext, _ := gin.CreateTestContext(userRecorder)
	userContext.Set("role", common.RoleCommonUser)
	GetFeedbackPage(userContext)
	assert.Equal(t, 403, userRecorder.Code)

	adminRecorder := httptest.NewRecorder()
	adminContext, _ := gin.CreateTestContext(adminRecorder)
	adminContext.Set("role", common.RoleAdminUser)
	GetFeedbackPage(adminContext)
	assert.Equal(t, 200, adminRecorder.Code)
}
