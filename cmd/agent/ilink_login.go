package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	qrterminal "github.com/mdp/qrterminal/v3"
)

type ilinkEndpointResponse struct {
	LoggedIn         bool   `json:"logged_in,omitempty"`
	QRCodeImgContent string `json:"qrcode_img_content,omitempty"`
}

var renderTerminalQR = func(text string, writer io.Writer) {
	qrterminal.GenerateHalfBlock(text, qrterminal.L, writer)
}

func runIlinkLogin(ctx context.Context, stdout, stderr io.Writer, options cliOptions) int {
	return runIlinkEndpoint(ctx, stdout, stderr, options, http.MethodPost, "/api/v1/weixin/ilink/login")
}

func runIlinkEndpoint(ctx context.Context, stdout, stderr io.Writer, options cliOptions, method, path string) int {
	serverURL := strings.TrimRight(strings.TrimSpace(options.serverURL), "/")
	if serverURL == "" {
		fmt.Fprintln(stderr, "server URL is required")
		return 1
	}
	request, err := http.NewRequestWithContext(ctx, method, serverURL+path, nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(payload))
		if message == "" {
			message = response.Status
		}
		fmt.Fprintln(stderr, message)
		return 1
	}

	maybeRenderIlinkQRCode(stderr, payload)

	formatted := payload
	if pretty, err := indentJSON(payload); err == nil {
		formatted = pretty
	}
	if len(formatted) > 0 && formatted[len(formatted)-1] != '\n' {
		formatted = append(formatted, '\n')
	}
	_, _ = stdout.Write(formatted)
	return 0
}

func maybeRenderIlinkQRCode(writer io.Writer, payload []byte) {
	if writer == nil {
		return
	}
	var response ilinkEndpointResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return
	}
	qrContent := strings.TrimSpace(response.QRCodeImgContent)
	if qrContent == "" || response.LoggedIn {
		return
	}
	fmt.Fprintln(writer, "请用微信扫描以下二维码：")
	renderTerminalQR(qrContent, writer)
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "二维码内容：")
	fmt.Fprintln(writer, qrContent)
}

func indentJSON(payload []byte) ([]byte, error) {
	var buffer bytes.Buffer
	if err := json.Indent(&buffer, payload, "", "  "); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
