package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetQuotaCache(c *gin.Context) {
	if h == nil || h.quotaCache == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "quota cache unavailable"})
		return
	}
	c.JSON(http.StatusOK, h.quotaCache.Snapshot())
}

func (h *Handler) RefreshQuotaCache(c *gin.Context) {
	if h == nil || h.quotaCache == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "quota cache unavailable"})
		return
	}
	var body quotaCacheRefreshRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	response, err := h.quotaCache.Refresh(c.Request.Context(), body)
	if err != nil {
		statusCode, message := quotaCacheHTTPError(err)
		c.JSON(statusCode, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, response)
}
