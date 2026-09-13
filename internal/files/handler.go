package files

import (
	"mime"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/pkg/apiresponse"
	"github.com/satym-in/tenant-saas-backend/pkg/reqctx"
)

type Handler struct {
	svc      *Service
	maxBytes int64
}

func NewHandler(svc *Service, maxBytes int64) *Handler {
	return &Handler{svc: svc, maxBytes: maxBytes}
}

func (h *Handler) Upload(c *gin.Context) {
	caller, ok := reqctx.RequireCaller(c)
	if !ok {
		return
	}
	if h.maxBytes > 0 {
		// Multipart framing adds overhead, so enforce the exact file size after
		// parsing and use a slightly larger request cap to avoid rejecting a valid
		// upload before the file header is available.
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxBytes+(1<<20))
	}
	header, err := c.FormFile("file")
	if err != nil {
		reqctx.BadRequest(c, "file is required")
		return
	}
	projectID, err := optionalUUID(c.PostForm("project_id"))
	if err != nil {
		reqctx.BadRequest(c, "invalid project_id")
		return
	}
	content, err := header.Open()
	if err != nil {
		reqctx.BadRequest(c, "could not read uploaded file")
		return
	}
	defer content.Close()

	file, err := h.svc.Upload(c.Request.Context(),
		audit.EntryFromRequest(c, audit.ActionFileUploaded),
		caller.TenantID, caller.UserID,
		UploadInput{ProjectID: projectID, Name: header.Filename, ContentType: header.Header.Get("Content-Type"), SizeBytes: header.Size, Content: content})
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusCreated, file)
}

func (h *Handler) List(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	projectID, err := optionalUUID(c.Query("project_id"))
	if err != nil {
		reqctx.BadRequest(c, "invalid project_id")
		return
	}
	page, pageSize := apiresponse.ParsePageQuery(c)
	items, total, err := h.svc.List(c.Request.Context(), projectID, page, pageSize)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.SuccessPaginated(c, items, apiresponse.NewPagination(page, pageSize, total))
}

func (h *Handler) Get(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	id, ok := reqctx.UUIDParam(c, "fileID")
	if !ok {
		return
	}
	file, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, file)
}

func (h *Handler) Download(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	id, ok := reqctx.UUIDParam(c, "fileID")
	if !ok {
		return
	}
	file, content, err := h.svc.Open(c.Request.Context(), id)
	if err != nil {
		reqctx.RespondError(c, err)
		return
	}
	defer content.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": file.OriginalName})
	c.Header("Content-Disposition", disposition)
	c.Header("Content-Length", strconv.FormatInt(file.SizeBytes, 10))
	c.DataFromReader(http.StatusOK, file.SizeBytes, file.ContentType, content, nil)
}

func (h *Handler) Delete(c *gin.Context) {
	if _, ok := reqctx.RequireCaller(c); !ok {
		return
	}
	id, ok := reqctx.UUIDParam(c, "fileID")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), audit.EntryFromRequest(c, audit.ActionFileDeleted), id); err != nil {
		reqctx.RespondError(c, err)
		return
	}
	apiresponse.Success(c, http.StatusOK, gin.H{"message": "file deleted"})
}

func optionalUUID(raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
