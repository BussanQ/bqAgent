package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"bqagent/internal/globalconfig"
)

func (handler *handler) handleChangePassword(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if !sameOriginRequest(request) {
		writeError(writer, http.StatusForbidden, chatResponse{Error: "cross-origin request is not allowed"})
		return
	}
	auth := handler.auth
	if auth == nil || !auth.required {
		writeError(writer, http.StatusForbidden, chatResponse{Error: "未启用密码登录，请先在全局配置中设置密码"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxWebUILoginBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "密码请求格式错误"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "密码请求格式错误"})
		return
	}
	length := utf8.RuneCountInString(input.NewPassword)
	if length < 6 || length > 128 || strings.TrimSpace(input.NewPassword) != input.NewPassword {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "新密码须为 6–128 个字符，且首尾不能有空格"})
		return
	}
	if input.NewPassword != input.ConfirmPassword {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "两次输入的新密码不一致"})
		return
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	// Recheck after taking the lock: another request may just have revoked this session.
	if !auth.authenticatedLocked(request) {
		writeError(writer, http.StatusUnauthorized, chatResponse{Error: "登录已失效，请重新登录"})
		return
	}
	provided := sha256.Sum256([]byte(input.CurrentPassword))
	if subtle.ConstantTimeCompare(provided[:], auth.passwordHash[:]) != 1 {
		writeError(writer, http.StatusForbidden, chatResponse{Error: "当前密码错误"})
		return
	}
	next := sha256.Sum256([]byte(input.NewPassword))
	if subtle.ConstantTimeCompare(next[:], auth.passwordHash[:]) == 1 {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "新密码不能与当前密码相同"})
		return
	}
	if handler.providers == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "全局配置不可用"})
		return
	}
	if _, err := handler.providers.UpdateWebUI(&globalconfig.WebUI{Password: input.NewPassword}); err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: "保存密码失败，请检查全局配置及文件权限"})
		return
	}
	// Do not change the live password or sessions unless persistence succeeded.
	auth.passwordHash = next
	auth.sessions = make(map[string]time.Time)
	http.SetCookie(writer, &http.Cookie{
		Name: webUIAuthCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(writer, http.StatusOK, map[string]bool{"changed": true})
}
