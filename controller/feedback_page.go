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
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const (
	feedbackPageURLKey         = "FeedbackPageURL"
	feedbackPageUserVisibleKey = "FeedbackPageUserVisible"
)

func validateFeedbackPageURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) > 2048 {
		return errors.New("feedback page URL is too long")
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("feedback page URL must be an HTTPS URL without credentials or fragments")
	}
	return nil
}

func feedbackPageSettings() (string, bool) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return strings.TrimSpace(common.OptionMap[feedbackPageURLKey]), common.OptionMap[feedbackPageUserVisibleKey] == "true"
}

func GetFeedbackPage(c *gin.Context) {
	pageURL, userVisible := feedbackPageSettings()
	if c.GetInt("role") < common.RoleAdminUser && (pageURL == "" || !userVisible) {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("Feedback page is not available"))
		return
	}
	common.ApiSuccess(c, gin.H{"url": pageURL, "configured": pageURL != ""})
}
