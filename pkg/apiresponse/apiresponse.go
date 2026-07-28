// Package apiresponse implements the standardized API response envelope
// defined in the architecture design (Phase 2, section 2.6): every endpoint
// returns {success, data, meta} on success or {success, error, meta} on
// failure, so API clients never need endpoint-specific response parsing.
package apiresponse

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Meta carries cross-cutting response metadata: the request ID (for
// correlating client-reported issues with server-side logs/traces) and,
// for paginated list endpoints, pagination details.
type Meta struct {
	RequestID  string      `json:"request_id,omitempty"`
	Pagination *Pagination `json:"pagination,omitempty"`
}

// Pagination describes a page of a larger result set.
type Pagination struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// NewPagination computes TotalPages from the given page/pageSize/total.
func NewPagination(page, pageSize int, total int64) *Pagination {
	totalPages := 0
	if pageSize > 0 {
		totalPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	return &Pagination{Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages}
}

// Default and maximum page sizes for list endpoints.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// ParsePageQuery reads the conventional ?page= and ?page_size= query
// parameters, clamping them to a sane range.
//
// The MaxPageSize ceiling is a denial-of-service control, not a style choice: an
// unbounded page_size lets one request ask for an entire tenant's table,
// exhausting server memory and saturating the database. Malformed values fall
// back to defaults rather than erroring, since a bad page number is not worth
// failing an otherwise valid read request over.
func ParsePageQuery(c *gin.Context) (page, pageSize int) {
	page = 1
	pageSize = DefaultPageSize

	if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(c.Query("page_size")); err == nil && v > 0 {
		pageSize = v
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return page, pageSize
}

// Offset converts a 1-based page number into a SQL OFFSET.
func Offset(page, pageSize int) int {
	if page < 1 {
		page = 1
	}
	return (page - 1) * pageSize
}

// APIError is the structured error payload returned in the envelope's
// "error" field.
type APIError struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

type successEnvelope struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Meta    Meta        `json:"meta"`
}

type errorEnvelope struct {
	Success bool     `json:"success"`
	Error   APIError `json:"error"`
	Meta    Meta     `json:"meta"`
}

func requestID(c *gin.Context) string {
	if id, ok := c.Get("request_id"); ok {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}

// Success writes a 2xx success envelope.
func Success(c *gin.Context, status int, data interface{}) {
	c.JSON(status, successEnvelope{
		Success: true,
		Data:    data,
		Meta:    Meta{RequestID: requestID(c)},
	})
}

// SuccessPaginated writes a 200 success envelope with pagination metadata.
func SuccessPaginated(c *gin.Context, data interface{}, pagination *Pagination) {
	c.JSON(http.StatusOK, successEnvelope{
		Success: true,
		Data:    data,
		Meta:    Meta{RequestID: requestID(c), Pagination: pagination},
	})
}

// Error writes an error envelope with the given HTTP status, machine-readable
// code, and human-readable message. It does not abort the Gin context;
// callers should return immediately after calling Error in a handler.
func Error(c *gin.Context, status int, code, message string, details ...interface{}) {
	var d interface{}
	if len(details) > 0 {
		d = details[0]
	}
	c.AbortWithStatusJSON(status, errorEnvelope{
		Success: false,
		Error:   APIError{Code: code, Message: message, Details: d},
		Meta:    Meta{RequestID: requestID(c)},
	})
}
