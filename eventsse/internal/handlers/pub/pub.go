package pub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/krateoplatformops/eventsse/internal/labels"
	"github.com/krateoplatformops/eventsse/internal/store"
	"github.com/rs/zerolog"
	clientv3 "go.etcd.io/etcd/client/v3"
	corev1 "k8s.io/api/core/v1"
)

func SSE(cli clientv3.Watcher) http.Handler {
	return &handler{
		cli: cli,
	}
}

var _ http.Handler = (*handler)(nil)

type handler struct {
	cli clientv3.Watcher
}

// @title EventSSE API
// @version 1.0
// @description This the Krateo EventSSE server.
// @BasePath /

// Health godoc
// @Summary SSE Endpoint
// @Description Get available events notifications
// @ID notifications
// @Produce  json
// @Success 200 {array} types.Event
// @Router /pub [get]
func (r *handler) ServeHTTP(wri http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	// === CORS Preflight ===
	if req.Method == http.MethodOptions {
		r.setCORSHeaders(wri)
		wri.WriteHeader(http.StatusNoContent)
		return
	}

	r.setCORSHeaders(wri)

	// === SSE Headers ===
	wri.Header().Set("Content-Type", "text/event-stream")
	wri.Header().Set("Cache-Control", "no-cache")
	wri.Header().Set("Connection", "keep-alive")
	wri.Header().Set("X-Accel-Buffering", "no")

	log := zerolog.New(os.Stdout).With().
		Str("service", "eventsse").
		Timestamp().
		Logger()

	f, ok := wri.(http.Flusher)
	if !ok {
		log.Error().Msg("http.ResponseWriter does not implement http.Flusher")
		http.Error(wri, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	fmt.Fprintln(wri, "event: connection-established")
	fmt.Fprintln(wri, "id: 88888888")
	fmt.Fprintf(wri, "data: %s\n\n", `{"info": "Ready to watch events"}`)
	f.Flush()

	watchChan := r.cli.Watch(ctx, store.RootKey, clientv3.WithPrefix())
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("SSE client disconnected")
			return

		case watchResp, ok := <-watchChan:
			if !ok {
				log.Warn().Msg("Etcd watch channel closed")
				return
			}

			for _, ev := range watchResp.Events {
				key := string(ev.Kv.Key)
				val := ev.Kv.Value
				if len(val) == 0 {
					continue
				}

				var obj corev1.Event
				if err := json.Unmarshal(val, &obj); err != nil {
					log.Warn().Str("key", key).Msgf("Decoding JSON event: %s", err.Error())
					continue
				}

				cid := labels.CompositionID(&obj)
				belongsToComposition := len(cid) > 0

				eventName := "krateo"
				if len(cid) > 0 {
					eventName = cid
				}

				zle := log.Debug().
					Str("id", key).
					Str("reason", obj.Reason).
					Str("message", obj.Message).
					Str("involvedObject.Name", obj.InvolvedObject.Name).
					Str("involvedObject.Namespace", obj.InvolvedObject.Namespace)

				if belongsToComposition {
					zle.Str("event", cid)
				} else {
					zle.Str("event", "krateo")
				}
				zle.Msg("Sending SSE")
				zle = nil

				fmt.Fprintf(wri, "event: %s\n", eventName)
				fmt.Fprintf(wri, "id: %s\n", key)
				fmt.Fprintf(wri, "data: %s\n\n", string(val))
				f.Flush()

				log.Debug().
					Str("event", eventName).
					Str("key", key).
					Msg("SSE sent")
			}
		}
	}
}

// setCORSHeaders aggiunge header CORS generali
func (r *handler) setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-Auth-Code, X-Krateo-TraceId")
	w.Header().Set("Access-Control-Expose-Headers", "Link,Authorization,Content-Type")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}
