package pub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/krateoplatformops/eventsse/internal/labels"
	"github.com/krateoplatformops/eventsse/internal/store"
	"github.com/rs/zerolog"
	clientv3 "go.etcd.io/etcd/client/v3"
	corev1 "k8s.io/api/core/v1"
)

const (
	defaultHeartbeatInterval = 25 * time.Second
	defaultEventQueueSize    = 256
)

func SSE(cli clientv3.Watcher) http.Handler {
	return &handler{cli: cli}
}

type handler struct {
	cli clientv3.Watcher
}

func (r *handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	// CORS
	if req.Method == http.MethodOptions {
		r.setCORSHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	r.setCORSHeaders(w)

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	log := zerolog.New(os.Stdout).With().
		Str("service", "eventsse").
		Timestamp().
		Logger()

	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Error().Msg("http.ResponseWriter does not implement http.Flusher")
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	eventCh := make(chan string, defaultEventQueueSize)
	defer close(eventCh)

	go func() {
		for evt := range eventCh {
			_, err := w.Write([]byte(evt))
			if err != nil {
				log.Info().Msg("Client disconnected (write error)")
				return
			}
			flusher.Flush()
		}
	}()

	// Evento iniziale
	initial := fmt.Sprintf("event: connection-established\nid: 88888888\ndata: %s\n\n", `{"info": "Ready to watch events"}`)
	select {
	case eventCh <- initial:
	case <-ctx.Done():
		return
	}

	heartbeat := time.NewTicker(defaultHeartbeatInterval)
	defer heartbeat.Stop()

	watchChan := r.cli.Watch(ctx, store.RootKey, clientv3.WithPrefix())

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("SSE client disconnected")
			return

		case <-heartbeat.C:
			select {
			case eventCh <- ": ping\n\n":
			default:
				log.Debug().Msg("Heartbeat skipped, client lento")
			}

		case watchResp, ok := <-watchChan:
			if !ok {
				log.Warn().Msg("Etcd watch channel closed")
				return
			}
			if err := watchResp.Err(); err != nil {
				log.Error().Err(err).Msg("Error from ETCD watch")
				continue
			}

			for _, ev := range watchResp.Events {
				val := ev.Kv.Value
				key := string(ev.Kv.Key)
				if len(val) == 0 {
					continue
				}

				var obj corev1.Event
				if err := json.Unmarshal(val, &obj); err != nil {
					log.Warn().Str("key", key).Msgf("Decoding JSON event: %s", err.Error())
					continue
				}

				cid := labels.CompositionID(&obj)
				eventName := "krateo"
				if cid != "" {
					eventName = cid
				}

				log.Debug().
					Str("id", key).
					Str("event", eventName).
					Str("reason", obj.Reason).
					Str("message", obj.Message).
					Str("involvedObject.Name", obj.InvolvedObject.Name).
					Str("involvedObject.Namespace", obj.InvolvedObject.Namespace).
					Msg("Queueing SSE event")

				payload := fmt.Sprintf("event: %s\nid: %s\ndata: %s\n\n", eventName, key, string(val))

				// invio non bloccante
				select {
				case eventCh <- payload:
				default:
					log.Warn().Str("event", eventName).Str("key", key).Msg("Dropping SSE event, client troppo lento")
				}
			}
		}
	}
}

func (r *handler) setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers",
		"Accept, Authorization, Content-Type, X-Auth-Code, X-Krateo-TraceId")
	w.Header().Set("Access-Control-Expose-Headers",
		"Link, Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}
