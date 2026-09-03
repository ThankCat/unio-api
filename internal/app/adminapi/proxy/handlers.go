// Package proxy 提供出站代理实体的 Admin API（网关中心 · 代理）：
//
//	GET    /proxies              列表（含渠道/账号引用计数）
//	POST   /proxies              创建
//	PATCH  /proxies/{id}         更新（密码空串=保持不变）
//	POST   /proxies/{id}/status  启停（停用后引用方回退直连）
//	DELETE /proxies/{id}         物理删除（被引用 → 409）
package proxy

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	proxyservice "github.com/ThankCat/unio-gateway/internal/service/admin/proxy"
)

// Handler 是代理模块的 HTTP 层。
type Handler struct {
	service *proxyservice.Service
}

// Register 注册代理路由。
func Register(r chi.Router, service *proxyservice.Service) {
	if service == nil {
		return
	}
	h := &Handler{service: service}
	r.Get("/proxies", h.list)
	r.Post("/proxies", h.create)
	r.Patch("/proxies/{id}", h.update)
	r.Post("/proxies/{id}/status", h.setStatus)
	r.Delete("/proxies/{id}", h.deleteProxy)
}

type proxyRequest struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int32  `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Note     string `json:"note"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	proxies, err := h.service.List(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, map[string]any{"proxies": proxies})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req proxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	created, err := h.service.Create(r.Context(), proxyInput(0, req))
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, created)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req proxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	updated, err := h.service.Update(r.Context(), proxyInput(id, req))
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, updated)
}

type statusRequest struct {
	Status string `json:"status"`
}

func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req statusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	updated, err := h.service.SetStatus(r.Context(), id, req.Status)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, updated)
}

func (h *Handler) deleteProxy(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Delete(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func proxyInput(id int64, req proxyRequest) proxyservice.Input {
	return proxyservice.Input{
		ID:       id,
		Name:     req.Name,
		Protocol: req.Protocol,
		Host:     req.Host,
		Port:     req.Port,
		Username: req.Username,
		Password: req.Password,
		Note:     req.Note,
	}
}
