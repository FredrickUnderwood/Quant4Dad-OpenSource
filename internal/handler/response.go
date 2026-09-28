package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository/datasource"
	"gorm.io/gorm"
)

type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toHTTPError maps internal errors to user-facing HTTP responses. The default
// branch returns "internal server error" so SQL / stack traces stay in logs.
func toHTTPError(err error) (int, ErrorResponse) {
	switch {
	case errors.Is(err, datasource.ErrNoProvider):
		return http.StatusServiceUnavailable, ErrorResponse{Code: 503, Message: "No market-data provider is installed. Import your own CSV data or register a provider."}
	case errors.Is(err, domain.ErrResourceConflict):
		return http.StatusConflict, ErrorResponse{Code: 409, Message: "内容已被其他操作更新，请重新加载后再保存"}
	case errors.Is(err, gorm.ErrRecordNotFound):
		return http.StatusNotFound, ErrorResponse{Code: 404, Message: "resource not found"}
	default:
		// Surface validation messages: short strings without sensitive bits.
		if msg := err.Error(); len(msg) > 0 && len(msg) < 200 {
			return http.StatusBadRequest, ErrorResponse{Code: 400, Message: msg}
		}
		return http.StatusInternalServerError, ErrorResponse{Code: 500, Message: "internal server error"}
	}
}

func ok(c *gin.Context, v any) {
	c.JSON(http.StatusOK, v)
}

func created(c *gin.Context, v any) {
	c.JSON(http.StatusCreated, v)
}
