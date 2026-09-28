package handler

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/gin-gonic/gin"

	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/strictjson"
)

var modelBodyJSON = sonic.Config{DisallowUnknownFields: true, CaseSensitive: true, ValidateString: true, UseUnicodeErrors: true}.Froze()
var modelETagPattern = regexp.MustCompile(`^"([0-9a-f]{32})"$`)

// RegisterAgentModels is wired only when agent.enabled is true. The existing
// GET/PUT settings routes retain their compatibility semantics in either mode.
func (h *SettingHandler) RegisterAgentModels(rg *gin.RouterGroup) {
	rg.PATCH("/settings/llm-providers/:provider", h.patchLLMProvider)
	rg.DELETE("/settings/llm-providers/:provider", h.deleteLLMProvider)
	rg.GET("/agent/models", h.getAgentModels)
}

func modelHeaders(c *gin.Context) { c.Header("Cache-Control", "no-store") }

func modelHTTPError(c *gin.Context, err error) {
	status, message := http.StatusInternalServerError, "internal server error"
	switch {
	case errors.Is(err, service.ErrModelInputInvalid):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrModelNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrModelRevisionStale):
		status, message = http.StatusPreconditionFailed, err.Error()
	case errors.Is(err, service.ErrModelConfigInvalid):
		message = err.Error()
	}
	c.JSON(status, ErrorResponse{Code: status, Message: message})
}

func writeModelView(c *gin.Context, view service.LLMProvidersView) {
	c.Header("ETag", `"`+view.Revision+`"`)
	ok(c, view)
}

// An optional strong If-Match protects editors from overwriting a revision they
// have not seen. The comparison runs inside the write transaction.
func modelIfMatch(c *gin.Context) (string, bool) {
	values := c.Request.Header.Values("If-Match")
	if len(values) == 0 {
		return "", true
	}
	if len(values) == 1 {
		if match := modelETagPattern.FindStringSubmatch(values[0]); match != nil {
			return match[1], true
		}
	}
	modelHTTPError(c, service.ErrModelInputInvalid)
	return "", false
}

// Never echo JSON parser errors: they may contain a replacement API key.
func decodeModelBody(c *gin.Context, dest any) bool {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, ErrorResponse{Code: 415, Message: "application/json required"})
		return false
	}
	const maxBody = 256 << 10
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxBody))
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			c.JSON(http.StatusRequestEntityTooLarge, ErrorResponse{Code: 413, Message: "request too large"})
		} else {
			modelHTTPError(c, service.ErrModelInputInvalid)
		}
		return false
	}
	var shape map[string]any
	if !utf8.Valid(body) || !validModelUnicodeEscapes(body) || strictjson.Decode(body, &shape, maxBody) != nil || shape == nil || containsModelNull(shape) ||
		modelBodyJSON.Unmarshal(bytes.TrimSpace(body), dest) != nil {
		modelHTTPError(c, service.ErrModelInputInvalid)
		return false
	}
	return true
}

// Sonic can replace unpaired UTF-16 escapes instead of reporting an error on
// some supported builds. Reject them before decoding, preserving credential
// bytes and model identities; a literal escaped backslash is not a Unicode escape.
func validModelUnicodeEscapes(body []byte) bool {
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			return false
		}
		if body[i] != 'u' {
			continue
		}
		if i+4 >= len(body) {
			return false
		}
		n, err := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func containsModelNull(v any) bool {
	if v == nil {
		return true
	}
	switch v := v.(type) {
	case map[string]any:
		for _, child := range v {
			if containsModelNull(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsModelNull(child) {
				return true
			}
		}
	}
	return false
}

func (h *SettingHandler) patchLLMProvider(c *gin.Context) {
	modelHeaders(c)
	var patch service.LLMProviderPatch
	if !decodeModelBody(c, &patch) {
		return
	}
	expected, valid := modelIfMatch(c)
	if !valid {
		return
	}
	view, err := h.svc.PatchLLMProvider(c.Request.Context(), c.Param("provider"), patch, expected)
	if err != nil {
		modelHTTPError(c, err)
		return
	}
	writeModelView(c, view)
}

func (h *SettingHandler) deleteLLMProvider(c *gin.Context) {
	modelHeaders(c)
	expected, valid := modelIfMatch(c)
	if !valid {
		return
	}
	view, err := h.svc.DeleteLLMProvider(c.Request.Context(), c.Param("provider"), expected)
	if err != nil {
		modelHTTPError(c, err)
		return
	}
	writeModelView(c, view)
}

func (h *SettingHandler) getAgentModels(c *gin.Context) {
	modelHeaders(c)
	catalog, err := h.svc.GetAgentModelCatalog(c.Request.Context())
	if err != nil {
		modelHTTPError(c, err)
		return
	}
	c.Header("ETag", `"`+catalog.Revision+`"`)
	ok(c, catalog)
}
