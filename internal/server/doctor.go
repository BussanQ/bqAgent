package server

import (
	"errors"
	"net/http"
	"time"

	"bqagent/internal/doctor"
)

func (handler *handler) handleReadiness(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ready := false
	if handler.service != nil && handler.service.doctor != nil {
		report, err := handler.service.doctor.Inspect(request.Context(), false)
		ready = err == nil && report.Ready
	}
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(writer, status, map[string]bool{"ready": ready})
}

func (handler *handler) handleDoctor(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "workspace unavailable"})
		return
	}
	if service.doctor == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "diagnostics unavailable"})
		return
	}
	report, err := service.doctor.Inspect(request.Context(), request.Method == http.MethodPost)
	if err != nil {
		status := http.StatusRequestTimeout
		if errors.Is(err, doctor.ErrProbeInProgress) {
			status = http.StatusConflict
		}
		writeError(writer, status, chatResponse{Error: "diagnostic cancelled, timed out, or already in progress"})
		return
	}
	for _, channel := range handler.channels {
		if channel == nil {
			continue
		}
		check := channelDiagnostic(channel)
		replaced := false
		for index := range report.Checks {
			if report.Checks[index].Group == "channels" && report.Checks[index].ID == check.ID {
				report.Checks[index] = check
				replaced = true
				break
			}
		}
		if !replaced {
			report.Checks = append(report.Checks, check)
		}
	}
	doctor.Summarize(&report)
	writeJSON(writer, http.StatusOK, report)
}

func channelDiagnostic(channel Channel) doctor.Check {
	check := doctor.Check{ID: channel.Name(), Group: "channels", State: "available", Reason: "adapter_available", Source: "runtime", CheckedAt: time.Now().UTC(), Hint: "No test message, login or reconnect is sent."}
	if !channel.Enabled() {
		check.State = "disabled"
		check.Reason = "disabled"
		return check
	}
	switch channel := channel.(type) {
	case *QQChannel:
		channel.mu.Lock()
		started, stopping, lastError := channel.started, channel.stopping, channel.diagnosticError
		channel.mu.Unlock()
		check.State = "unverified"
		check.Reason = "connection_not_observed"
		if !channel.Configured() {
			check.State = "error"
			check.Reason = "missing_credentials"
		} else if !started || stopping {
			check.State = "unverified"
			check.Reason = "not_running"
		} else if live, ok := channel.gateway.(interface{ Connected() bool }); ok && live.Connected() {
			check.State = "available"
			check.Reason = "gateway_connected"
		} else if lastError != "" {
			check.State = "error"
			check.Reason = lastError
		}
	case *IlinkChannel:
		status := channel.Status()
		check.State = "unverified"
		check.Reason = "not_logged_in"
		if status.PollerRunning {
			check.State = "available"
			check.Reason = "poller_running"
		}
		if status.LastError != "" {
			check.State = "error"
			check.Reason = "poller_login_or_storage_failed"
		}
	case *ServerChanChannel:
		if channel.botWebhookProcessor != nil && channel.botWebhookProcessor.Configured() {
			check.Reason = "adapter_and_bot_configured"
		} else {
			check.Reason = "adapter_available_bot_not_configured"
		}
		channel.mu.Lock()
		stopping := channel.stopping
		channel.mu.Unlock()
		if stopping {
			check.State = "unverified"
			check.Reason = "stopping"
		}
	}
	return check
}
