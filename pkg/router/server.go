package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"
)

const (
	PROXY_REQUEST_READ_TIMEOUT = 5 * time.Second
)

type VersionRequest struct {
	Version int
}

func StartServer(ctx context.Context, log *zap.Logger, manager *Manager, serverPort int, script string) {
	client := &http.Client{
		Timeout: PROXY_REQUEST_READ_TIMEOUT,
	}

	http.HandleFunc("/__meta/version", func(resp http.ResponseWriter, req *http.Request) {
		var versionReq VersionRequest

		err := json.NewDecoder(req.Body).Decode(&versionReq)
		if err != nil {
			httpErr(log, resp, err, "failed to read proxy request body")
			return
		}

		err = manager.StartProcess(ctx, versionReq.Version)
		if err != nil {
			httpErr(log, resp, err, "failed to start process")
			return
		}

		fmt.Fprintf(resp, "Version: %d", versionReq.Version)
	})

	http.HandleFunc("/", func(resp http.ResponseWriter, req *http.Request) {
		reqCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		portChan := manager.LivePortChannel(reqCtx)

		select {
		case <-time.After(PROXY_REQUEST_READ_TIMEOUT):
			http.Error(resp, "timeout waiting for live port", http.StatusGatewayTimeout)

		case port := <-portChan:
			body, err := io.ReadAll(req.Body)
			if err != nil {
				httpErr(log, resp, err, "failed to read proxy request body")
				return
			}

			url := fmt.Sprintf("http://%s:%d/", manager.Host, port)

			proxyReq, err := http.NewRequest(req.Method, url, bytes.NewReader(body))
			if err != nil {
				httpErr(log, resp, err, "failed to create proxy request")
				return
			}

			proxyReq.Header = make(http.Header)
			copyHeader(proxyReq.Header, req.Header)

			manager.IncrementRequestCounter(port)
			defer manager.DecrementRequestCounter(port)

			proxyResp, err := client.Do(proxyReq)
			if err != nil {
				httpErr(log, resp, err, "failed to proxy request")
				return
			}
			defer proxyResp.Body.Close()

			copyHeader(resp.Header(), proxyResp.Header)
			io.Copy(resp, proxyResp.Body)
		}
	})

	log.Info("start routing server", zap.Int("port", serverPort), zap.String("script", script))
	http.ListenAndServe("127.0.0.1:"+strconv.Itoa(serverPort), nil)
}

func copyHeader(dest, src http.Header) {
	for key, value := range src {
		for _, nested := range value {
			dest.Add(key, nested)
		}
	}
}

func httpErr(log *zap.Logger, resp http.ResponseWriter, err error, message string) {
	log.Error(message, zap.Error(err))
	http.Error(resp, err.Error(), http.StatusInternalServerError)
}
